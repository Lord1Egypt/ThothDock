# F-JAVA: Eclipse Temurin 21 JDK (HotSpot JIT, GC threads, large reserved
# heap): the source-file launcher compiles and runs Probe.java.
# timeout: 300
IMG=eclipse-temurin:21-jdk-alpine
dk pull -q $IMG >/dev/null
setup
dk run -i --rm --network "$N" $IMG sh -c 'cat > /tmp/Probe.java && cd /tmp && java -Xmx256m Probe.java' < Probe.java > "$TMPD/java.out" 2>&1
code=$?; cat "$TMPD/java.out"; ok $code "java exits 0"
no_leftovers
