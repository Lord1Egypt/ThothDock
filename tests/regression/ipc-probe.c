/* System V IPC semantics probe for PRoot --sysvipc (ThothDock).
 *
 *   ipc-probe all          every check below, one PASS/FAIL line each
 *   ipc-probe postgres     PostgreSQL's interlock sequence only
 *   ipc-probe hold SECONDS create a segment and a semaphore, then sleep
 *
 * Exit status: 0 when every check passed, 1 otherwise. Built static so it
 * runs unchanged in any rootfs. */
#define _GNU_SOURCE
#include <errno.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ipc.h>
#include <sys/msg.h>
#include <sys/resource.h>
#include <sys/sem.h>
#include <sys/shm.h>
#include <sys/time.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>
#include <pthread.h>

#if defined(__BIONIC__)
/* bionic has no strerrorname_np. */
static const char *strerrorname_np(int e)
{
	switch (e) {
	case ENOENT: return "ENOENT"; case EINVAL: return "EINVAL"; case EEXIST: return "EEXIST";
	case EAGAIN: return "EAGAIN"; case EINTR: return "EINTR"; case EIDRM: return "EIDRM";
	case ENOMSG: return "ENOMSG"; case ENOSYS: return "ENOSYS"; case ENOSPC: return "ENOSPC";
	case ENOMEM: return "ENOMEM"; case EACCES: return "EACCES"; case EPERM: return "EPERM";
	default: return "E?";
	}
}
#else
union semun { int val; struct semid_ds *buf; unsigned short *array; };
#endif

static int failures;

static void check(int ok, const char *what)
{
	printf("%s %s\n", ok ? "PASS" : "FAIL", what);
	fflush(stdout);
	if (!ok)
		failures++;
}

static void check_errno(long rc, int want, const char *what)
{
	char buf[160];
	snprintf(buf, sizeof buf, "%s (rc=%ld errno=%s, want %s)", what, rc,
		 rc == -1 ? strerrorname_np(errno) : "-", strerrorname_np(want));
	check(rc == -1 && errno == want, buf);
}

static double now(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return ts.tv_sec + ts.tv_nsec / 1e9;
}

static int child_status(pid_t pid)
{
	int st;
	if (waitpid(pid, &st, 0) != pid)
		return -1;
	return WIFEXITED(st) ? WEXITSTATUS(st) : 128 + WTERMSIG(st);
}

/* The sequence PostgreSQL 17 runs in sysv_shmem.c / dsm_impl.c. */
static void postgres_interlock(void)
{
	key_t key = 0x26CC1D;  /* the key of the reported failure: 2542621 */
	int id = shmget(key, 56, IPC_CREAT | IPC_EXCL | 0600);
	check(id >= 0, "postgres: shmget(key, 56, 03600) creates the interlock segment");
	if (id < 0) {
		printf("     errno=%s\n", strerrorname_np(errno));
		return;
	}
	check_errno(shmget(key, 56, IPC_CREAT | IPC_EXCL | 0600), EEXIST,
		    "postgres: a second IPC_EXCL create is refused");
	char *p = shmat(id, NULL, 0);
	if (p == (void *)-1)
		printf("     shmat errno=%s\n", strerrorname_np(errno));
	check(p != (void *)-1, "postgres: shmat attaches");
	if (p == (void *)-1)
		return;
	memset(p, 0, 56);
	strcpy(p, "PGSHMEM");
	struct shmid_ds ds;
	check(shmctl(id, IPC_STAT, &ds) == 0 && ds.shm_nattch == 1 && ds.shm_segsz == 56,
	      "postgres: IPC_STAT reports nattch 1 and size 56");
	check(shmdt(p) == 0, "postgres: shmdt");
	check(shmctl(id, IPC_STAT, &ds) == 0 && ds.shm_nattch == 0,
	      "postgres: IPC_STAT reports nattch 0 after detach");
	check(shmctl(id, IPC_RMID, NULL) == 0, "postgres: IPC_RMID");
	check_errno(shmget(key, 0, 0), ENOENT, "postgres: key is gone after IPC_RMID");
}

static void shm_checks(void)
{
	check_errno(shmget(0x7777, 4096, 0600), ENOENT, "shm: lookup of a missing key");

	int id = shmget(IPC_PRIVATE, 1 << 20, IPC_CREAT | 0600);
	check(id >= 0, "shm: 1 MiB IPC_PRIVATE segment");
	if (id < 0)
		return;
	unsigned char *p = shmat(id, NULL, 0);
	check(p != (void *)-1, "shm: attach");
	if (p == (void *)-1)
		return;
	check(p[0] == 0 && p[(1 << 20) - 1] == 0, "shm: new segment is zero-filled");
	p[0] = 0xA5;
	p[(1 << 20) - 1] = 0x5A;

	/* A forked child inherits the attachment and sees the parent's bytes. */
	pid_t c = fork();
	if (c == 0)
		_exit(p[0] == 0xA5 && p[(1 << 20) - 1] == 0x5A ? (p[1] = 0x11, 0) : 1);
	check(child_status(c) == 0 && p[1] == 0x11, "shm: inherited mapping is shared both ways");

	/* A child that attaches by id on its own sees the same memory. */
	c = fork();
	if (c == 0) {
		unsigned char *q = shmat(id, NULL, 0);
		if (q == (void *)-1)
			_exit(2);
		int ok = q[0] == 0xA5 && q[1] == 0x11;
		q[2] = 0x22;
		shmdt(q);
		_exit(ok ? 0 : 1);
	}
	check(child_status(c) == 0 && p[2] == 0x22, "shm: independent attach by id sees the same bytes");

	/* Two attachments in one process alias the same pages. */
	unsigned char *p2 = shmat(id, NULL, 0);
	check(p2 != (void *)-1 && p2 != p && (p2[0] = 0x33, p[0] == 0x33), "shm: second attach aliases the first");
	struct shmid_ds ds;
	check(shmctl(id, IPC_STAT, &ds) == 0 && ds.shm_nattch == 2, "shm: nattch counts both attachments");
	if (p2 != (void *)-1)
		shmdt(p2);

	/* IPC_RMID while attached: memory stays usable until the last detach. */
	check(shmctl(id, IPC_RMID, NULL) == 0, "shm: IPC_RMID while attached");
	p[3] = 0x44;
	check(p[3] == 0x44, "shm: still usable after IPC_RMID until detached");
	check(shmdt(p) == 0, "shm: last detach");
	check_errno(shmdt(p), EINVAL, "shm: detaching an address twice");
	check_errno((long)(intptr_t)shmat(id, NULL, 0) == -1 ? -1 : 0, EINVAL,
		    "shm: attach after removal");

	/* Keyed lookups find the same segment; a size mismatch is refused. */
	key_t key = 0x5157;
	int k1 = shmget(key, 8192, IPC_CREAT | 0600);
	int k2 = shmget(key, 0, 0600);
	check(k1 >= 0 && k1 == k2, "shm: keyed lookup returns the same id");
	check_errno(shmget(key, 65536, 0600), EINVAL, "shm: lookup with a larger size");
	if (k1 >= 0)
		check(shmctl(k1, IPC_RMID, NULL) == 0, "shm: remove keyed segment");

	/* Detach happens at exit: a child that exits attached leaves nattch 0. */
	id = shmget(IPC_PRIVATE, 4096, IPC_CREAT | 0600);
	c = fork();
	if (c == 0) {
		void *q = shmat(id, NULL, 0);
		_exit(q == (void *)-1);
	}
	child_status(c);
	check(shmctl(id, IPC_STAT, &ds) == 0 && ds.shm_nattch == 0, "shm: exit detaches");
	shmctl(id, IPC_RMID, NULL);
}

static void *attach_from_thread(void *arg)
{
	int id = *(int *)arg;
	unsigned char *q = shmat(id, NULL, 0);
	if (q == (void *)-1)
		return (void *)1;
	q[0] = 0x77;
	return (void *)(intptr_t)shmdt(q);
}

/* Many processes attach at the same moment; each must get the right segment. */
static void shm_concurrency(void)
{
	enum { N = 16 };
	int id = shmget(IPC_PRIVATE, 4096 * 4, IPC_CREAT | 0600);
	int decoy = shmget(IPC_PRIVATE, 4096, IPC_CREAT | 0600);
	check(id >= 0 && decoy >= 0, "shm concurrency: two segments");
	unsigned char *p = shmat(id, NULL, 0), *d = shmat(decoy, NULL, 0);
	if (p == (void *)-1 || d == (void *)-1)
		return;
	memset(p, 0, 4096 * 4);
	memset(d, 0xEE, 4096);
	int go[2];
	if (pipe(go) != 0)
		return;
	pid_t kids[N];
	for (int i = 0; i < N; i++) {
		kids[i] = fork();
		if (kids[i] == 0) {
			char c;
			close(go[1]);
			if (read(go[0], &c, 1) != 0)
				_exit(3);
			int target = i % 2 ? decoy : id;
			unsigned char *q = shmat(target, NULL, 0);
			if (q == (void *)-1)
				_exit(2);
			if (target == id)
				q[100 + i] = (unsigned char)(i + 1);
			else if (q[0] != 0xEE)
				_exit(1);
			shmdt(q);
			_exit(0);
		}
	}
	close(go[0]);
	close(go[1]); /* all children start at once */
	int ok = 1;
	for (int i = 0; i < N; i++)
		ok &= child_status(kids[i]) == 0;
	for (int i = 0; i < N; i += 2)
		ok &= p[100 + i] == i + 1;
	for (int i = 1; i < N; i += 2)
		ok &= p[100 + i] == 0;
	check(ok, "shm concurrency: 16 simultaneous attaches each reach their own segment");

	pthread_t t;
	void *rv = (void *)1;
	if (pthread_create(&t, NULL, attach_from_thread, &id) == 0)
		pthread_join(t, &rv);
	check(rv == NULL && p[0] == 0x77, "shm: attach from a secondary thread");
	shmdt(p);
	shmdt(d);
	shmctl(id, IPC_RMID, NULL);
	shmctl(decoy, IPC_RMID, NULL);
}

static double cpu_seconds_children(void)
{
	struct rusage ru;
	getrusage(RUSAGE_CHILDREN, &ru);
	return ru.ru_utime.tv_sec + ru.ru_stime.tv_sec + (ru.ru_utime.tv_usec + ru.ru_stime.tv_usec) / 1e6;
}

static void sem_checks(void)
{
	int id = semget(IPC_PRIVATE, 2, IPC_CREAT | 0600);
	check(id >= 0, "sem: semget of a 2-semaphore set");
	if (id < 0)
		return;
	union semun arg = { .val = 3 };
	check(semctl(id, 0, SETVAL, arg) == 0 && semctl(id, 0, GETVAL) == 3, "sem: SETVAL/GETVAL");

	struct sembuf dec = { 0, -3, 0 };
	check(semop(id, &dec, 1) == 0 && semctl(id, 0, GETVAL) == 0, "sem: semop decrement");
	struct sembuf nowait = { 0, -1, IPC_NOWAIT };
	check_errno(semop(id, &nowait, 1), EAGAIN, "sem: IPC_NOWAIT on a zero semaphore");

	struct timespec ts = { 0, 300 * 1000 * 1000 };
	double t0 = now();
	struct sembuf wait1 = { 0, -1, 0 };
	long rc = semtimedop(id, &wait1, 1, &ts);
	double dt = now() - t0;
	char buf[96];
	snprintf(buf, sizeof buf, "sem: semtimedop times out with EAGAIN (%.2fs, rc=%ld errno=%s)", dt, rc,
		 rc == -1 ? strerrorname_np(errno) : "-");
	check(rc == -1 && errno == EAGAIN && dt >= 0.25 && dt < 2.0, buf);

	/* A blocked waiter in another process wakes when we post, and sleeps
	 * (no CPU) while blocked. */
	double cpu0 = cpu_seconds_children();
	pid_t c = fork();
	if (c == 0) {
		struct sembuf w = { 0, -1, 0 };
		_exit(semop(id, &w, 1) == 0 ? 0 : 1);
	}
	usleep(700 * 1000);
	int still_blocked = waitpid(c, NULL, WNOHANG) == 0;
	struct sembuf post = { 0, 1, 0 };
	t0 = now();
	semop(id, &post, 1);
	int st = child_status(c);
	dt = now() - t0;
	double cpu = cpu_seconds_children() - cpu0;
	snprintf(buf, sizeof buf, "sem: cross-process wake-up (woke in %.3fs, waiter CPU %.3fs)", dt, cpu);
	check(still_blocked && st == 0 && dt < 1.0 && cpu < 0.1, buf);

	/* Removing the set wakes waiters with EIDRM. */
	c = fork();
	if (c == 0) {
		struct sembuf w = { 1, -1, 0 };
		int r = semop(id, &w, 1);
		_exit(r == -1 && errno == EIDRM ? 0 : 1);
	}
	usleep(300 * 1000);
	check(semctl(id, 0, IPC_RMID) == 0, "sem: IPC_RMID");
	check(child_status(c) == 0, "sem: a waiter gets EIDRM when the set is removed");
	check_errno(semctl(id, 0, GETVAL), EINVAL, "sem: GETVAL on a removed set");
}

static void msg_checks(void)
{
	int id = msgget(IPC_PRIVATE, IPC_CREAT | 0600);
	check(id >= 0, "msg: msgget");
	if (id < 0)
		return;
	struct { long type; char text[16]; } m = { 7, "hello" }, r = { 0 };
	check(msgsnd(id, &m, sizeof m.text, 0) == 0, "msg: msgsnd");
	pid_t c = fork();
	if (c == 0) {
		struct { long type; char text[16]; } x = { 0 };
		_exit(msgrcv(id, &x, sizeof x.text, 7, 0) > 0 && strcmp(x.text, "hello") == 0 ? 0 : 1);
	}
	check(child_status(c) == 0, "msg: another process receives the message");
	check_errno(msgrcv(id, &r, sizeof r.text, 0, IPC_NOWAIT), ENOMSG, "msg: empty queue with IPC_NOWAIT");
	check(msgctl(id, IPC_RMID, NULL) == 0, "msg: IPC_RMID");
}

int main(int argc, char **argv)
{
	const char *mode = argc > 1 ? argv[1] : "all";
	if (strcmp(mode, "postgres") == 0) {
		postgres_interlock();
	} else if (strcmp(mode, "late") == 0) {
		/* A workload that reloads on SIGHUP (PostgreSQL does) and creates
		 * segments later on (dynamic shared memory). */
		signal(SIGHUP, SIG_IGN);
		int first = shmget(IPC_PRIVATE, 4096, IPC_CREAT | 0600);
		check(first >= 0, "late: first segment");
		sleep(argc > 2 ? atoi(argv[2]) : 3);
		int id = shmget(IPC_PRIVATE, 4096, IPC_CREAT | 0600);
		char *p = id >= 0 ? shmat(id, NULL, 0) : (void *)-1;
		char buf[96];
		snprintf(buf, sizeof buf, "late: segment created and attached after the wait (errno=%s)",
			 p == (void *)-1 ? strerrorname_np(errno) : "-");
		check(p != (void *)-1, buf);
	} else if (strcmp(mode, "hold") == 0) {
		int sid = shmget(IPC_PRIVATE, 65536, IPC_CREAT | 0600);
		char *p = sid >= 0 ? shmat(sid, NULL, 0) : (void *)-1;
		int semid = semget(IPC_PRIVATE, 1, IPC_CREAT | 0600);
		check(p != (void *)-1 && semid >= 0, "hold: segment and semaphore created");
		fflush(stdout);
		sleep(argc > 2 ? atoi(argv[2]) : 60);
	} else {
		postgres_interlock();
		shm_checks();
		shm_concurrency();
		sem_checks();
		msg_checks();
	}
	printf("%s: %d failure(s)\n", mode, failures);
	return failures != 0;
}
