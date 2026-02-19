// gen_pprof.go generates pprof fixture files for testing.
//
// Usage:
//
//	go run testdata/gen/gen_pprof.go
//
// Output files (written to testdata/):
//
//	cpu.pb.gz   — CPU profile (~200ms workload)
//	cpu2.pb.gz  — second CPU profile (for diff tests)
//	alloc.pb.gz — heap allocation profile
//	alloc2.pb.gz — second alloc profile (for diff tests)
//	mutex.pb.gz — mutex contention profile

//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"
)

//go:noinline
func pprofBusyCompute() int {
	sum := 0
	for i := 0; i < 5_000_000; i++ {
		sum += i * i
	}
	return sum
}

//go:noinline
func pprofBusySort() {
	data := make([]int, 100_000)
	for i := range data {
		data[i] = len(data) - i
	}
	for i := 0; i < len(data); i++ {
		for j := i + 1; j < len(data) && j < i+100; j++ {
			if data[j] < data[i] {
				data[i], data[j] = data[j], data[i]
			}
		}
	}
}

//go:noinline
func pprofAllocSlices() {
	var keep [][]byte
	for i := 0; i < 10_000; i++ {
		keep = append(keep, make([]byte, 1024))
	}
	runtime.KeepAlive(keep)
}

//go:noinline
func pprofAllocMaps() {
	var keep []map[string]int
	for i := 0; i < 5_000; i++ {
		m := make(map[string]int, 100)
		for j := 0; j < 100; j++ {
			m["key"] = j
		}
		keep = append(keep, m)
	}
	runtime.KeepAlive(keep)
}

//go:noinline
func pprofLockContention(mu *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()
	for i := 0; i < 500; i++ {
		mu.Lock()
		sum := 0
		for j := 0; j < 1000; j++ {
			sum += j
		}
		runtime.KeepAlive(sum)
		mu.Unlock()
	}
}

func generateCPU(path string) {
	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		panic(err)
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		pprofBusyCompute()
		pprofBusySort()
	}
	pprof.StopCPUProfile()
	f.Close()
	fmt.Printf("  %s\n", path)
}

func generateAlloc(path string) {
	runtime.GC()
	pprofAllocSlices()
	pprofAllocMaps()

	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	p := pprof.Lookup("allocs")
	if p == nil {
		panic("alloc profile not available")
	}
	if err := p.WriteTo(f, 0); err != nil {
		panic(err)
	}
	fmt.Printf("  %s\n", path)
}

func generateMutex(path string) {
	prev := runtime.SetMutexProfileFraction(1)
	defer runtime.SetMutexProfileFraction(prev)

	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go pprofLockContention(&mu, &wg)
	}
	wg.Wait()

	f, err := os.Create(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	p := pprof.Lookup("mutex")
	if p == nil {
		panic("mutex profile not available")
	}
	if err := p.WriteTo(f, 0); err != nil {
		panic(err)
	}
	fmt.Printf("  %s\n", path)
}

func main() {
	dir := filepath.Join("testdata")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: run from repository root (testdata/ not found)\n")
		os.Exit(1)
	}

	fmt.Println("Generating pprof fixtures...")
	generateCPU(filepath.Join(dir, "cpu.pb.gz"))
	generateCPU(filepath.Join(dir, "cpu2.pb.gz"))
	generateAlloc(filepath.Join(dir, "alloc.pb.gz"))
	generateAlloc(filepath.Join(dir, "alloc2.pb.gz"))
	generateMutex(filepath.Join(dir, "mutex.pb.gz"))
	fmt.Println("Done.")
}
