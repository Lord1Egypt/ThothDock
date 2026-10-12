/* Core Linux compatibility probe for ThothDock containers.
 *
 *   core-probe [process|memory|sync|fs|net|all]
 *
 * One line per check: PASS/FAIL for behaviour a Linux container must have,
 * INFO for platform facts that are reported, not judged (virtual address
 * width, POSIX shared memory availability, IPv6, external DNS). Exit status
 * 1 when any FAIL. Built static so it runs in any image. */
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <errno.h>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <poll.h>
#include <pthread.h>
#include <semaphore.h>
#include <setjmp.h>
#include <signal.h>
#include <spawn.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/file.h>
#include <sys/mman.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/time.h>
#include <sys/un.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>
#include <linux/futex.h>

#ifndef MAP_FIXED_NOREPLACE
#define MAP_FIXED_NOREPLACE 0x100000
#endif

extern char **environ;
static int failures;
static char dir[] = "/tmp/core-probe.XXXXXX";

static void check(int ok, const char *fmt, ...) __attribute__((format(printf, 2, 3)));
static void check(int ok, const char *fmt, ...)
{
	va_list ap;
	va_start(ap, fmt);
	printf("%s ", ok ? "PASS" : "FAIL");
	vprintf(fmt, ap);
	printf("\n");
	fflush(stdout);
	va_end(ap);
	if (!ok)
		failures++;
}
#define INFO(...) (printf("INFO " __VA_ARGS__), printf("\n"), fflush(stdout))

static double now(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return ts.tv_sec + ts.tv_nsec / 1e9;
}

static int wait_status(pid_t pid)
{
	int st;
	if (waitpid(pid, &st, 0) != pid)
		return -1;
	return WIFEXITED(st) ? WEXITSTATUS(st) : 128 + WTERMSIG(st);
}

static char *path(const char *name)
{
	static char buf[4][256];
	static int n;
	char *b = buf[n++ % 4];
	snprintf(b, 256, "%s/%s", dir, name);
	return b;
}

/* ------------------------------------------------------------- process */
static volatile sig_atomic_t got_usr1;
static void on_usr1(int s) { (void)s; got_usr1 = 1; }

static void process_checks(void)
{
	int codes[] = { 0, 3, 255 }, all = 1;
	for (int i = 0; i < 3; i++) {
		pid_t c = fork();
		if (c == 0)
			_exit(codes[i]);
		all &= wait_status(c) == codes[i];
	}
	check(all, "process: fork/wait exit codes 0, 3, 255");

	pid_t c = fork();
	if (c == 0) {
		pause();
		_exit(0);
	}
	usleep(100000);
	kill(c, SIGTERM);
	check(wait_status(c) == 128 + SIGTERM, "process: child killed by SIGTERM reports the signal");

	struct sigaction sa = { .sa_handler = on_usr1 };
	sigaction(SIGUSR1, &sa, NULL);
	kill(getpid(), SIGUSR1);
	check(got_usr1, "process: signal handler runs (SIGUSR1)");

	pid_t v = vfork();
	if (v == 0) {
		execl("/bin/sh", "sh", "-c", "exit 7", (char *)NULL);
		_exit(127);
	}
	check(wait_status(v) == 7, "process: vfork + execve");

	pid_t sp;
	char *argv[] = { "sh", "-c", "exit 9", NULL };
	check(posix_spawn(&sp, "/bin/sh", NULL, NULL, argv, environ) == 0 && wait_status(sp) == 9,
	      "process: posix_spawn (clone with CLONE_VM|CLONE_VFORK)");

	c = fork();
	if (c == 0) {
		execl("/nonexistent/binary", "x", (char *)NULL);
		_exit(errno == ENOENT ? 0 : 1);
	}
	check(wait_status(c) == 0, "process: execve of a missing file fails with ENOENT");

	int p[2];
	pipe(p);
	c = fork();
	if (c == 0) {
		close(p[0]);
		char buf[65536];
		memset(buf, 'x', sizeof buf);
		_exit(write(p[1], buf, sizeof buf) == sizeof buf ? 0 : 1);
	}
	close(p[1]);
	size_t total = 0;
	ssize_t n;
	char buf[4096];
	while ((n = read(p[0], buf, sizeof buf)) > 0)
		total += n;
	close(p[0]);
	check(wait_status(c) == 0 && total == 65536, "process: 64 KiB through a pipe");

	/* A grandchild that outlives its parent is still reaped by someone:
	 * here we only check that waiting on the parent does not block on it. */
	c = fork();
	if (c == 0) {
		if (fork() == 0) {
			usleep(200000);
			_exit(0);
		}
		_exit(0);
	}
	double t0 = now();
	check(wait_status(c) == 0 && now() - t0 < 0.15, "process: parent exit does not wait for its orphan");

	sigset_t set;
	sigemptyset(&set);
	sigaddset(&set, SIGUSR2);
	sigprocmask(SIG_BLOCK, &set, NULL);
	kill(getpid(), SIGUSR2);
	struct timespec to = { 1, 0 };
	check(sigtimedwait(&set, NULL, &to) == SIGUSR2, "process: sigtimedwait receives a blocked signal");
	sigprocmask(SIG_UNBLOCK, &set, NULL);
}

/* -------------------------------------------------------------- memory */
static sigjmp_buf segv_jmp;
static void on_segv(int s) { (void)s; siglongjmp(segv_jmp, 1); }

static void memory_checks(void)
{
	size_t pg = sysconf(_SC_PAGESIZE);
	INFO("memory: page size %zu", pg);
	char *m = mmap(NULL, 4 * pg, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	check(m != MAP_FAILED && m[0] == 0 && (m[4 * pg - 1] = 1), "memory: anonymous mmap is zeroed and writable");

	struct sigaction sa = { .sa_handler = on_segv }, old;
	sigaction(SIGSEGV, &sa, &old);
	int faulted = 0;
	mprotect(m, pg, PROT_READ);
	if (sigsetjmp(segv_jmp, 1) == 0)
		*(volatile char *)m = 1;
	else
		faulted = 1;
	sigaction(SIGSEGV, &old, NULL);
	check(faulted, "memory: mprotect(PROT_READ) makes a write fault (SIGSEGV)");
	check(munmap(m, 4 * pg) == 0, "memory: munmap");

	int *sh = mmap(NULL, pg, PROT_READ | PROT_WRITE, MAP_SHARED | MAP_ANONYMOUS, -1, 0);
	*sh = 0;
	pid_t c = fork();
	if (c == 0) {
		*sh = 42;
		_exit(0);
	}
	wait_status(c);
	check(*sh == 42, "memory: MAP_SHARED|MAP_ANONYMOUS is shared with a child");

	int fd = open(path("mapped"), O_RDWR | O_CREAT | O_TRUNC, 0600);
	ftruncate(fd, pg);
	char *fm = mmap(NULL, pg, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
	strcpy(fm, "mapped-write");
	msync(fm, pg, MS_SYNC);
	char back[16] = { 0 };
	pread(fd, back, 12, 0);
	check(strcmp(back, "mapped-write") == 0, "memory: MAP_SHARED file mapping writes reach the file");
	munmap(fm, pg);
	close(fd);

	char *big = mmap(NULL, 8UL << 30, PROT_NONE, MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE, -1, 0);
	check(big != MAP_FAILED, "memory: 8 GiB PROT_NONE reservation (JVM, WiredTiger style)");
	if (big != MAP_FAILED)
		munmap(big, 8UL << 30);

	char *al = mmap(NULL, 2UL << 30, PROT_NONE, MAP_PRIVATE | MAP_ANONYMOUS | MAP_NORESERVE, -1, 0);
	if (al != MAP_FAILED) {
		uintptr_t a = ((uintptr_t)al + (1UL << 30) - 1) & ~((1UL << 30) - 1);
		check(mprotect((void *)a, pg, PROT_READ | PROT_WRITE) == 0 && (*(char *)a = 1),
		      "memory: 1 GiB-aligned region carved from a reservation");
		munmap(al, 2UL << 30);
	} else {
		check(0, "memory: 1 GiB-aligned region carved from a reservation");
	}

	char *dn = mmap(NULL, pg, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	dn[0] = 7;
	madvise(dn, pg, MADV_DONTNEED);
	check(dn[0] == 0, "memory: MADV_DONTNEED drops private anonymous pages");
	munmap(dn, pg);

	/* Userspace virtual address width: where a 1 GiB fixed reservation
	 * still fits. Reported, not judged (MongoDB 8's TCMalloc needs 48). */
	int bits = 0;
	for (int b = 36; b <= 47; b++) {
		void *h = (void *)((1UL << b) - (2UL << 30));
		void *r = mmap(h, 1UL << 30, PROT_NONE, MAP_PRIVATE | MAP_ANONYMOUS | MAP_FIXED_NOREPLACE, -1, 0);
		if (r == h) {
			bits = b;
			munmap(r, 1UL << 30);
		}
	}
	INFO("memory: userspace virtual address space reaches %d bits", bits);

	/* POSIX shared memory lives in /dev/shm (shm_open, sem_open). */
	int sfd = shm_open("/core-probe", O_RDWR | O_CREAT | O_EXCL, 0600);
	if (sfd >= 0) {
		shm_unlink("/core-probe");
		close(sfd);
		INFO("memory: POSIX shm_open works (/dev/shm present)");
	} else {
		INFO("memory: POSIX shm_open fails: %s (/dev/shm %s)", strerror(errno),
		     access("/dev/shm", F_OK) == 0 ? "exists" : "missing");
	}
	sem_t *ns = sem_open("/core-probe-sem", O_CREAT | O_EXCL, 0600, 1);
	if (ns != SEM_FAILED) {
		sem_close(ns);
		sem_unlink("/core-probe-sem");
		INFO("memory: POSIX named semaphores (sem_open) work");
	} else {
		INFO("memory: POSIX sem_open fails: %s", strerror(errno));
	}
}

/* ---------------------------------------------------------------- sync */
static pthread_mutex_t mu = PTHREAD_MUTEX_INITIALIZER;
static long counter;
static void *bump(void *arg)
{
	(void)arg;
	for (int i = 0; i < 100000; i++) {
		pthread_mutex_lock(&mu);
		counter++;
		pthread_mutex_unlock(&mu);
	}
	return NULL;
}

static void sync_checks(void)
{
	pthread_t t[4];
	for (int i = 0; i < 4; i++)
		pthread_create(&t[i], NULL, bump, NULL);
	for (int i = 0; i < 4; i++)
		pthread_join(t[i], NULL);
	check(counter == 400000, "sync: pthread mutex across 4 threads");

	struct shared { pthread_mutex_t m; sem_t s; long n; uint32_t futex; } *sh =
		mmap(NULL, sizeof *sh, PROT_READ | PROT_WRITE, MAP_SHARED | MAP_ANONYMOUS, -1, 0);
	pthread_mutexattr_t ma;
	pthread_mutexattr_init(&ma);
	pthread_mutexattr_setpshared(&ma, PTHREAD_PROCESS_SHARED);
	pthread_mutex_init(&sh->m, &ma);
	sem_init(&sh->s, 1, 0);
	pid_t kids[2];
	for (int k = 0; k < 2; k++) {
		kids[k] = fork();
		if (kids[k] == 0) {
			for (int i = 0; i < 50000; i++) {
				pthread_mutex_lock(&sh->m);
				sh->n++;
				pthread_mutex_unlock(&sh->m);
			}
			_exit(0);
		}
	}
	wait_status(kids[0]);
	wait_status(kids[1]);
	check(sh->n == 100000, "sync: process-shared mutex across 2 processes");

	pid_t c = fork();
	if (c == 0)
		_exit(sem_wait(&sh->s) == 0 ? 0 : 1);
	usleep(200000);
	sem_post(&sh->s);
	check(wait_status(c) == 0, "sync: process-shared unnamed semaphore (sem_init pshared)");

	sh->futex = 0;
	c = fork();
	if (c == 0) {
		while (__atomic_load_n(&sh->futex, __ATOMIC_ACQUIRE) == 0)
			syscall(SYS_futex, &sh->futex, FUTEX_WAIT, 0, NULL, NULL, 0);
		_exit(0);
	}
	usleep(200000);
	__atomic_store_n(&sh->futex, 1, __ATOMIC_RELEASE);
	syscall(SYS_futex, &sh->futex, FUTEX_WAKE, 1, NULL, NULL, 0);
	double t0 = now();
	check(wait_status(c) == 0 && now() - t0 < 1, "sync: futex wait/wake across processes");

	struct timespec rel = { 0, 200 * 1000 * 1000 };
	uint32_t f = 0;
	t0 = now();
	long r = syscall(SYS_futex, &f, FUTEX_WAIT, 0, &rel, NULL, 0);
	double dt = now() - t0;
	check(r == -1 && errno == ETIMEDOUT && dt > 0.15 && dt < 1.0, "sync: futex timeout (%.2fs)", dt);

	int fd = open(path("lock"), O_RDWR | O_CREAT, 0600);
	flock(fd, LOCK_EX);
	c = fork();
	if (c == 0) {
		int fd2 = open(path("lock"), O_RDWR);
		_exit(flock(fd2, LOCK_EX | LOCK_NB) == -1 && errno == EWOULDBLOCK ? 0 : 1);
	}
	check(wait_status(c) == 0, "sync: flock excludes another process");
	flock(fd, LOCK_UN);

	struct flock fl = { .l_type = F_WRLCK, .l_whence = SEEK_SET, .l_start = 0, .l_len = 10 };
	fcntl(fd, F_SETLK, &fl);
	c = fork();
	if (c == 0) {
		int fd2 = open(path("lock"), O_RDWR);
		struct flock g = { .l_type = F_WRLCK, .l_whence = SEEK_SET, .l_start = 5, .l_len = 1 };
		int busy = fcntl(fd2, F_SETLK, &g) == -1 && (errno == EAGAIN || errno == EACCES);
		struct flock h = { .l_type = F_WRLCK, .l_whence = SEEK_SET, .l_start = 20, .l_len = 1 };
		_exit(busy && fcntl(fd2, F_SETLK, &h) == 0 ? 0 : 1);
	}
	check(wait_status(c) == 0, "sync: fcntl record locks (overlap refused, disjoint granted)");

	struct flock o = { .l_type = F_WRLCK, .l_whence = SEEK_SET, .l_start = 100, .l_len = 1 };
	int fd3 = open(path("lock"), O_RDWR);
	int ofd = fcntl(fd3, F_OFD_SETLK, &o) == 0;
	int fd4 = open(path("lock"), O_RDWR);
	int ofd_busy = fcntl(fd4, F_OFD_SETLK, &o) == -1 && errno == EAGAIN;
	check(ofd && ofd_busy, "sync: open file description locks (F_OFD_SETLK)");
	close(fd3);
	close(fd4);
	close(fd);
}

/* ------------------------------------------------------------------ fs */
static void fs_checks(void)
{
	int fd = open(path("data"), O_RDWR | O_CREAT | O_TRUNC, 0640);
	check(write(fd, "0123456789", 10) == 10 && fsync(fd) == 0 && fdatasync(fd) == 0,
	      "fs: write + fsync + fdatasync");
	close(fd);

	fd = open(path("new"), O_WRONLY | O_CREAT | O_TRUNC, 0600);
	write(fd, "new", 3);
	close(fd);
	check(rename(path("new"), path("data")) == 0, "fs: rename replaces an existing file");
	char b[8] = { 0 };
	fd = open(path("data"), O_RDONLY);
	read(fd, b, 7);
	close(fd);
	check(strcmp(b, "new") == 0, "fs: renamed content is visible");

	struct stat st;
	check(link(path("data"), path("hard")) == 0 && stat(path("data"), &st) == 0 && st.st_nlink == 2,
	      "fs: hard link (nlink 2)");
	fd = open(path("hard"), O_WRONLY | O_APPEND);
	write(fd, "+", 1);
	close(fd);
	stat(path("data"), &st);
	check(st.st_size == 4, "fs: a write through the hard link shows in the original");
	check(unlink(path("data")) == 0 && stat(path("hard"), &st) == 0 && st.st_nlink == 1,
	      "fs: unlinking one name keeps the other");

	char target[64] = { 0 };
	check(symlink("hard", path("sym")) == 0 && readlink(path("sym"), target, sizeof target - 1) == 4 &&
		      strcmp(target, "hard") == 0 && lstat(path("sym"), &st) == 0 && S_ISLNK(st.st_mode),
	      "fs: symlink + readlink + lstat");

	check(chmod(path("hard"), 0751) == 0 && stat(path("hard"), &st) == 0 && (st.st_mode & 07777) == 0751,
	      "fs: chmod is reflected by stat");

	check(mkdir(path("sub"), 0755) == 0 && mkdir(path("sub/inner"), 0700) == 0 &&
		      rmdir(path("sub/inner")) == 0 && rmdir(path("sub")) == 0,
	      "fs: mkdir/rmdir");
	check(rmdir(dir) == -1 && errno == ENOTEMPTY, "fs: rmdir of a non-empty directory fails with ENOTEMPTY");

	fd = open(dir, O_TMPFILE | O_RDWR, 0600);
	if (fd >= 0) {
		check(write(fd, "t", 1) == 1, "fs: O_TMPFILE");
		close(fd);
	} else {
		INFO("fs: O_TMPFILE unavailable: %s", strerror(errno));
	}

	struct timespec ts[2] = { { 1000000000, 0 }, { 1000000000, 0 } };
	check(utimensat(AT_FDCWD, path("hard"), ts, 0) == 0 && stat(path("hard"), &st) == 0 &&
		      st.st_mtime == 1000000000,
	      "fs: utimensat sets mtime");
	check(truncate(path("hard"), 1) == 0 && stat(path("hard"), &st) == 0 && st.st_size == 1, "fs: truncate");
}

/* ----------------------------------------------------------------- net */
static void *echo_once(void *arg)
{
	int ls = *(int *)arg;
	int c = accept(ls, NULL, NULL);
	char buf[64];
	ssize_t n = read(c, buf, sizeof buf);
	if (n > 0)
		write(c, buf, n);
	close(c);
	return NULL;
}

static int tcp_echo(int family)
{
	int ls = socket(family, SOCK_STREAM, 0);
	struct sockaddr_storage ss = { 0 };
	socklen_t len;
	if (family == AF_INET) {
		struct sockaddr_in *a = (void *)&ss;
		a->sin_family = AF_INET;
		a->sin_addr.s_addr = htonl(INADDR_LOOPBACK);
		len = sizeof *a;
	} else {
		struct sockaddr_in6 *a = (void *)&ss;
		a->sin6_family = AF_INET6;
		a->sin6_addr = in6addr_loopback;
		len = sizeof *a;
	}
	if (bind(ls, (void *)&ss, len) || listen(ls, 4) || getsockname(ls, (void *)&ss, &len))
		return close(ls), -1;
	pthread_t t;
	pthread_create(&t, NULL, echo_once, &ls);
	int c = socket(family, SOCK_STREAM, 0);
	int ok = connect(c, (void *)&ss, len) == 0 && write(c, "ping", 4) == 4;
	char buf[8] = { 0 };
	ok = ok && read(c, buf, 4) == 4 && strcmp(buf, "ping") == 0;
	close(c);
	pthread_join(t, NULL);
	close(ls);
	return ok;
}

static void net_checks(void)
{
	check(tcp_echo(AF_INET) == 1, "net: TCP echo over 127.0.0.1 (ephemeral port)");
	int v6 = tcp_echo(AF_INET6);
	if (v6 == 1)
		INFO("net: TCP over ::1 works");
	else
		INFO("net: TCP over ::1 unavailable");

	int ls = socket(AF_UNIX, SOCK_STREAM, 0);
	struct sockaddr_un ua = { .sun_family = AF_UNIX };
	snprintf(ua.sun_path, sizeof ua.sun_path, "%s", path("sock"));
	int ok = bind(ls, (void *)&ua, sizeof ua) == 0 && listen(ls, 1) == 0;
	int c = socket(AF_UNIX, SOCK_STREAM, 0);
	ok = ok && connect(c, (void *)&ua, sizeof ua) == 0;
	int a = accept(ls, NULL, NULL);
	ok = ok && write(c, "u", 1) == 1;
	char ch = 0;
	ok = ok && read(a, &ch, 1) == 1 && ch == 'u';
	check(ok, "net: UNIX stream socket at a filesystem path");
	close(a);
	close(c);
	close(ls);

	int sv[2];
	ok = socketpair(AF_UNIX, SOCK_STREAM, 0, sv) == 0;
	int ep = epoll_create1(0);
	struct epoll_event ev = { .events = EPOLLIN, .data.fd = sv[0] }, out;
	epoll_ctl(ep, EPOLL_CTL_ADD, sv[0], &ev);
	double t0 = now();
	int n0 = epoll_wait(ep, &out, 1, 200);
	double dt = now() - t0;
	write(sv[1], "e", 1);
	int n1 = epoll_wait(ep, &out, 1, 1000);
	check(ok && n0 == 0 && dt > 0.15 && dt < 1.0 && n1 == 1 && out.data.fd == sv[0],
	      "net: epoll timeout (%.2fs) and readiness", dt);
	struct pollfd pfd = { .fd = sv[0], .events = POLLIN };
	char drain;
	read(sv[0], &drain, 1);
	t0 = now();
	int p0 = poll(&pfd, 1, 200);
	dt = now() - t0;
	check(p0 == 0 && dt > 0.15 && dt < 1.0, "net: poll timeout (%.2fs)", dt);
	close(ep);
	close(sv[0]);
	close(sv[1]);

	/* Non-blocking connect to a closed local port fails with ECONNREFUSED. */
	int s = socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK, 0);
	struct sockaddr_in closed = { .sin_family = AF_INET, .sin_port = htons(1), .sin_addr.s_addr = htonl(INADDR_LOOPBACK) };
	connect(s, (void *)&closed, sizeof closed);
	struct pollfd cp = { .fd = s, .events = POLLOUT };
	poll(&cp, 1, 2000);
	int err = 0;
	socklen_t el = sizeof err;
	getsockopt(s, SOL_SOCKET, SO_ERROR, &err, &el);
	check(err == ECONNREFUSED, "net: non-blocking connect to a closed port reports ECONNREFUSED (%s)", strerror(err));
	close(s);

	struct addrinfo *res = NULL, hints = { .ai_socktype = SOCK_STREAM };
	check(getaddrinfo("localhost", "80", &hints, &res) == 0, "net: getaddrinfo(localhost)");
	if (res)
		freeaddrinfo(res);
	res = NULL;
	int g = getaddrinfo("dns.google", "443", &hints, &res);
	if (g == 0)
		INFO("net: external DNS resolves (dns.google)");
	else
		INFO("net: external DNS failed: %s", gai_strerror(g));
	if (res)
		freeaddrinfo(res);
}

int main(int argc, char **argv)
{
	const char *what = argc > 1 ? argv[1] : "all";
	if (!mkdtemp(dir)) {
		perror("mkdtemp");
		return 2;
	}
	int all = strcmp(what, "all") == 0;
	if (all || !strcmp(what, "process"))
		process_checks();
	if (all || !strcmp(what, "memory"))
		memory_checks();
	if (all || !strcmp(what, "sync"))
		sync_checks();
	if (all || !strcmp(what, "fs"))
		fs_checks();
	if (all || !strcmp(what, "net"))
		net_checks();
	char cmd[300];
	snprintf(cmd, sizeof cmd, "rm -rf %s", dir);
	system(cmd);
	printf("%s: %d failure(s)\n", what, failures);
	return failures != 0;
}
