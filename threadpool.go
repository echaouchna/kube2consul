package main

import (
	"runtime"
	"sync"
)

type JobFunc func(int, interface{})

func threadMain(id int, queue chan interface{}, wg *sync.WaitGroup, job JobFunc) chan bool {
	quitCommand := make(chan bool, 1)
	go func() {
		for {
			select {
			case task := <-queue:
				wg.Add(1)
				job(id, task)
				wg.Done()
			case <-quitCommand:
				return
			}

		}
	}()
	return quitCommand
}

func RunWorkers(queue chan interface{}, job JobFunc) func() {
	var wg sync.WaitGroup
	cpuCount := runtime.NumCPU()
	runtime.GOMAXPROCS(cpuCount)

	quitCommands := make([]chan bool, cpuCount)
	for i := 0; i < cpuCount; i++ {
		quitCommands[i] = threadMain(i+1, queue, &wg, job)
	}
	return func() {
		for _, quitCommand := range quitCommands {
			quitCommand <- true
		}
		wg.Wait()
	}
}
