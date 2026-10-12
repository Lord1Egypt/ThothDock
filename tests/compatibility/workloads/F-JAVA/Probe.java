// Runtime fixture for F-JAVA (source-file launcher): threads, ProcessBuilder,
// pipes, files, an HTTP server and client, DNS, HTTPS.
import com.sun.net.httpserver.HttpServer;
import java.io.*;
import java.net.*;
import java.net.http.*;
import java.nio.file.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.*;

public class Probe {
    static void check(boolean ok, String what) { System.out.println((ok ? "PASS " : "FAIL ") + what); }
    public static void main(String[] a) throws Exception {
        ExecutorService ex = Executors.newFixedThreadPool(8);
        AtomicLong sum = new AtomicLong();
        for (int i = 0; i < 16; i++) ex.submit(() -> { long s = 0; for (int j = 0; j < 1_000_000; j++) s += j; sum.addAndGet(s); });
        ex.shutdown(); ex.awaitTermination(60, TimeUnit.SECONDS);
        check(sum.get() == 16L * 499999500000L, "java: 16 tasks on 8 threads");
        Process p = new ProcessBuilder("sh", "-c", "echo sub; exit 3").start();
        String out = new String(p.getInputStream().readAllBytes());
        check(out.equals("sub\n") && p.waitFor() == 3, "java: ProcessBuilder output and exit code");
        Process t = new ProcessBuilder("tr", "a-z", "A-Z").start();
        t.getOutputStream().write("piped".getBytes()); t.getOutputStream().close();
        check(new String(t.getInputStream().readAllBytes()).equals("PIPED"), "java: pipes to a child");
        Path d = Files.createTempDirectory("j"); Path f = d.resolve("f");
        Files.write(f, new byte[1 << 20]); Files.move(f, d.resolve("g"), StandardCopyOption.ATOMIC_MOVE);
        check(Files.size(d.resolve("g")) == 1 << 20, "java: write and atomic move 1 MiB");
        HttpServer srv = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        srv.createContext("/", e -> { byte[] b = "loop".getBytes(); e.sendResponseHeaders(200, b.length); e.getResponseBody().write(b); e.close(); });
        srv.start();
        HttpClient c = HttpClient.newBuilder().connectTimeout(java.time.Duration.ofSeconds(15)).build();
        String body = c.send(HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + srv.getAddress().getPort() + "/")).build(), HttpResponse.BodyHandlers.ofString()).body();
        check(body.equals("loop"), "java: HTTP server and client over loopback");
        srv.stop(0);
        try { InetAddress.getByName("dns.google"); check(true, "java: DNS lookup"); } catch (Exception e) { check(false, "java: DNS lookup (" + e + ")"); }
        try {
            String r = c.send(HttpRequest.newBuilder(URI.create("https://dns.google/resolve?name=example.com")).build(), HttpResponse.BodyHandlers.ofString()).body();
            check(r.contains("Answer"), "java: HTTPS request (certificate verified)");
        } catch (Exception e) { check(false, "java: HTTPS request (" + e + ")"); }
    }
}
