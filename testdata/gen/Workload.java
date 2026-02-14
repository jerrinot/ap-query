/**
 * Workload program for generating JFR test fixtures with async-profiler.
 * Each workload runs in a named thread so tests can verify thread filtering.
 * Methods are split into two levels to give call trees depth.
 *
 * Usage: java Workload
 * Profile with: java -agentpath:/path/to/libasyncProfiler.so=start,event=cpu,file=out.jfr Workload
 */
public class Workload {

    static volatile long sink;
    static final Object LOCK = new Object();
    static final int DURATION_MS = 5000;

    public static void main(String[] args) throws Exception {
        Thread cpu = new Thread(Workload::cpuWork, "cpu-worker");
        Thread alloc = new Thread(Workload::allocWork, "alloc-worker");
        Thread lock1 = new Thread(Workload::lockWork, "lock-worker-1");
        Thread lock2 = new Thread(Workload::lockWork, "lock-worker-2");
        Thread lock3 = new Thread(Workload::lockWork, "lock-worker-3");
        Thread wall = new Thread(Workload::wallWork, "wall-worker");

        cpu.start();
        alloc.start();
        lock1.start();
        lock2.start();
        lock3.start();
        wall.start();

        cpu.join();
        alloc.join();
        lock1.join();
        lock2.join();
        lock3.join();
        wall.join();
    }

    // --- CPU workload ---

    static void cpuWork() {
        long end = System.currentTimeMillis() + DURATION_MS;
        while (System.currentTimeMillis() < end) {
            computeStep();
        }
    }

    static void computeStep() {
        long x = 0;
        for (int i = 0; i < 100_000; i++) {
            x += i * 31L + (x ^ (x >>> 7));
        }
        sink = x;
    }

    // --- Allocation workload ---

    static void allocWork() {
        long end = System.currentTimeMillis() + DURATION_MS;
        while (System.currentTimeMillis() < end) {
            allocateObjects();
        }
    }

    static void allocateObjects() {
        for (int i = 0; i < 100; i++) {
            byte[] buf = new byte[4096];
            sink = buf.length;
        }
    }

    // --- Lock contention workload ---

    static void lockWork() {
        long end = System.currentTimeMillis() + DURATION_MS;
        while (System.currentTimeMillis() < end) {
            lockStep();
        }
    }

    static void lockStep() {
        synchronized (LOCK) {
            long x = 0;
            for (int i = 0; i < 10_000; i++) {
                x += i;
            }
            sink = x;
        }
    }

    // --- Wall-clock workload ---

    static void wallWork() {
        long end = System.currentTimeMillis() + DURATION_MS;
        while (System.currentTimeMillis() < end) {
            sleepStep();
        }
    }

    static void sleepStep() {
        try {
            Thread.sleep(50);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
