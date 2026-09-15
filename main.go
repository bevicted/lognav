package main

import (
	"os"
	"runtime/pprof"
	"runtime/trace"

	"github.com/bevicted/lognav/internal/cmd"
	"github.com/bevicted/lognav/internal/logging"
)

func main() {
	if _, ok := os.LookupEnv("LOGNAV_TRACE"); ok {
		traceF, err := os.Create("trace.out")
		if err != nil {
			panic(err)
		}
		if err := trace.Start(traceF); err != nil {
			panic(err)
		}
		defer trace.Stop()
	}

	closeLog, err := logging.Init()
	if err != nil {
		panic(err)
	}
	defer closeLog() //nolint:errcheck // log close failure not actionable on shutdown

	if _, ok := os.LookupEnv("LOGNAV_CPUPROF"); ok {
		cpuF, err := os.Create("cpu.pprof")
		if err != nil {
			panic(err)
		}
		if err := pprof.StartCPUProfile(cpuF); err != nil {
			panic(err)
		}
		defer cpuF.Close()
		defer pprof.StopCPUProfile()
	}

	if _, ok := os.LookupEnv("LOGNAV_MEMPROF"); ok {
		memF, err := os.Create("mem.pprof")
		if err != nil {
			panic(err)
		}
		defer memF.Close()
		defer pprof.WriteHeapProfile(memF) //nolint:errcheck // best-effort memprof dump on exit
	}

	if err := cmd.Execute(); err != nil {
		os.Exit(cmd.ExitCode(err)) //nolint:gocritic // profile dump is best-effort; renderExit already printed to stderr
	}
}
