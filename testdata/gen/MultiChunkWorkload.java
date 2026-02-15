/**
 * Workload program for generating a multi-chunk CPU JFR fixture.
 *
 * The hot method alternates every second between phaseA() and phaseB() so the
 * resulting profile contains significant samples from both methods across
 * multiple chunks.
 */
public class MultiChunkWorkload {

    static volatile long sink;
    static final int DURATION_MS = 18_000;

    public static void main(String[] args) {
        long end = System.currentTimeMillis() + DURATION_MS;
        long nextSwitch = System.currentTimeMillis() + 1_000;
        boolean useA = true;
        long x = 1;

        while (System.currentTimeMillis() < end) {
            long now = System.currentTimeMillis();
            if (now >= nextSwitch) {
                useA = !useA;
                nextSwitch += 1_000;
            }
            x = useA ? phaseA(x) : phaseB(x);
        }

        sink = x;
    }

    static long phaseA(long x) {
        for (int i = 0; i < 200_000; i++) {
            x = x * 1664525L + 1013904223L;
            x ^= (x >>> 13);
        }
        return x;
    }

    static long phaseB(long x) {
        for (int i = 0; i < 200_000; i++) {
            x ^= (x << 7);
            x += 0x9e3779b97f4a7c15L;
            x ^= (x >>> 11);
        }
        return x;
    }
}
