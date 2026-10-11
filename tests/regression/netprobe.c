/* Outbound connections from a container on a user-defined network.
 *
 *   netprobe IPV4_ADDRESS PORT
 *
 * A client that bind()s the IPv4 wildcard before it connects (musl's resolver
 * does) must still reach an address outside loopback. PRoot --net-ip once
 * moved that bind to the container's 127.77.x.y address, and the kernel then
 * refused the connect with EINVAL: DNS and every outbound connection of such
 * clients failed. Use a routable address that is not one of the machine's own
 * (192.0.2.1): connecting to a local address succeeds even from a loopback
 * source. Prints one "<case>: ok|<errno text>" line per case; a refused or
 * timed-out TCP connect still proves the path works (no listener is needed).
 */
#include <arpa/inet.h>
#include <errno.h>
#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>

static void probe(const char *what, int type, int bind_first, int v6, const char *ip, int port) {
    int s = socket(v6 ? AF_INET6 : AF_INET, type, 0);
    int r;
    if (s < 0) { printf("%s: socket: %s\n", what, strerror(errno)); return; }
    struct timeval tv = {2, 0}; /* an unanswered TCP connect must not block the test */
    setsockopt(s, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv);
    if (v6) {
        struct sockaddr_in6 b = {0}, d = {0};
        b.sin6_family = d.sin6_family = AF_INET6;
        d.sin6_port = htons(port);
        /* an IPv4 destination on a dual-stack socket */
        inet_pton(AF_INET6, "::ffff:0.0.0.0", &d.sin6_addr);
        char mapped[64]; snprintf(mapped, sizeof mapped, "::ffff:%s", ip);
        inet_pton(AF_INET6, mapped, &d.sin6_addr);
        if (bind_first && bind(s, (void *)&b, sizeof b) < 0) { printf("%s: bind: %s\n", what, strerror(errno)); close(s); return; }
        r = connect(s, (void *)&d, sizeof d);
    } else {
        struct sockaddr_in b = {0}, d = {0};
        b.sin_family = d.sin_family = AF_INET;
        d.sin_port = htons(port);
        inet_pton(AF_INET, ip, &d.sin_addr);
        if (bind_first && bind(s, (void *)&b, sizeof b) < 0) { printf("%s: bind: %s\n", what, strerror(errno)); close(s); return; }
        r = connect(s, (void *)&d, sizeof d);
    }
    printf("%s: %s\n", what, r == 0 ? "ok" : strerror(errno));
    close(s);
}

int main(int argc, char **argv) {
    if (argc != 3) { fprintf(stderr, "usage: netprobe IPV4 PORT\n"); return 2; }
    int port = atoi(argv[2]);
    probe("udp, connect only", SOCK_DGRAM, 0, 0, argv[1], port);
    probe("udp, bind wildcard:0 first", SOCK_DGRAM, 1, 0, argv[1], port);
    probe("tcp, connect only", SOCK_STREAM, 0, 0, argv[1], port);
    probe("tcp, bind wildcard:0 first", SOCK_STREAM, 1, 0, argv[1], port);
    probe("udp6 (dual-stack), bind [::]:0 first", SOCK_DGRAM, 1, 1, argv[1], port);
    probe("tcp6 (dual-stack), bind [::]:0 first", SOCK_STREAM, 1, 1, argv[1], port);
    return 0;
}
