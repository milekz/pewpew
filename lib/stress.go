package pewpew

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

//TODO move to other file
type workerDone struct{}

type (
	//StressConfig is the top level struct that contains the configuration for a stress test
	StressConfig struct {
		Verbose bool
		Quiet   bool

		//Global Timeout
		GlobalTimeout int
		//Count is how many total requests to make for each Target
		Count int
		//Concurrency is how many requests can be happening simultaneously for each Target
		Concurrency int
		Targets     []Target

		//global target settings

		DNSPrefetch     bool
		Timeout         string
		Method          string
		Body            string
		BodyFilename    string
		Headers         string
		Cookies         string
		UserAgent       string
		BasicAuth       string
		Compress        bool
		KeepAlive       bool
		FollowRedirects bool
		NoHTTP2         bool
		EnforceSSL      bool
	}
)

//NewStressConfig creates a new StressConfig
//with package defaults
func NewStressConfig() (s *StressConfig) {
	s = &StressConfig{
		Count:         DefaultCount,
		Concurrency:   DefaultConcurrency,
		GlobalTimeout: DefaultGlobalTimeout,
		Targets: []Target{
			{
				URL:             DefaultURL,
				Timeout:         DefaultTimeout,
				Method:          DefaultMethod,
				UserAgent:       DefaultUserAgent,
				FollowRedirects: true,
			},
		},
	}
	return
}

//RunStress starts the stress tests with the provided StressConfig.
//Throughout the test, data is sent to w, useful for live updates.
func RunStress(s StressConfig, w io.Writer) ([][]RequestStat, error) {
	if w == nil {
		return nil, errors.New("nil writer")
	}
	err := validateStressConfig(s)
	if err != nil {
		return nil, errors.New("invalid configuration: " + err.Error())
	}
	targetCount := len(s.Targets)

	Rununtil := time.Now().Unix() + int64(s.GlobalTimeout)
	//setup printer
	p := printer{output: w}

	//setup the queue of requests, one queue per target
	requestQueues := make([](chan http.Request), targetCount)
	for idx, target := range s.Targets {
		requestQueue, err := createRequestQueue(s.Count, target)
		if err != nil {
			return nil, err
		}
		requestQueues[idx] = requestQueue
	}

	if targetCount == 1 {
		fmt.Fprintf(w, "Stress testing %d target:\n", targetCount)
	} else {
		fmt.Fprintf(w, "Stress testing %d targets:\n", targetCount)
	}

	//when a target is finished, send all stats into this
	targetStats := make(chan []RequestStat)
	for idx, target := range s.Targets {
		go func(target Target, requestQueue chan http.Request, targetStats chan []RequestStat) {
			p.writeString(fmt.Sprintf("- Running %d tests at %s, %d at a time\n", s.Count, target.URL, s.Concurrency))

			workerDoneChan := make(chan workerDone)   //workers use this to indicate they are done
			requestStatChan := make(chan RequestStat) //workers communicate each requests' info

			client := createClient(target)

			//start up the workers
			for i := 0; i < s.Concurrency; i++ {
				go func() {
					for {
						select {
						case req, ok := <-requestQueue:
							if !ok {
								//queue is empty
								workerDoneChan <- workerDone{}
								return
							}
							response, stat := runRequest(req, client)
							if Rununtil <= time.Now().Unix() {
								//We reach GlobalTimeout
								workerDoneChan <- workerDone{}
								return
							}
							if !s.Quiet {
								p.printStat(stat)
								if s.Verbose {
									p.printVerbose(&req, response)
								}
							}

							requestStatChan <- stat
						}
					}
				}()
			}
			requestStats := make([]RequestStat, s.Count)
			requestsCompleteCount := 0
			workersDoneCount := 0
			//wait for all workers to finish
			for {
				select {
				case <-workerDoneChan:
					workersDoneCount++
				case stat := <-requestStatChan:
					requestStats[requestsCompleteCount] = stat
					requestsCompleteCount++
				}
				if workersDoneCount == s.Concurrency {
					//all workers are finished
					break
				}
			}
			targetStats <- requestStats
		}(target, requestQueues[idx], targetStats)
	}
	targetRequestStats := make([][]RequestStat, targetCount)
	targetDoneCount := 0
	for {
		select {
		case reqStats := <-targetStats:
			targetRequestStats[targetDoneCount] = reqStats
			targetDoneCount++
		}
		if targetDoneCount == targetCount {
			//all targets are finished
			break
		}
	}

	return targetRequestStats, nil
}

func validateStressConfig(s StressConfig) error {
	if len(s.Targets) == 0 {
		return errors.New("zero targets")
	}
	if s.Count <= 0 {
		return errors.New("request count must be greater than zero")
	}
	if s.Concurrency <= 0 {
		return errors.New("concurrency must be greater than zero")
	}
	if s.Concurrency > s.Count {
		return errors.New("concurrency must be higher than request count")
	}

	for _, target := range s.Targets {
		if err := validateTarget(target); err != nil {
			return err
		}
	}
	return nil
}

// createRequestQueue creates a channel of http.Requests of size count
func createRequestQueue(count int, target Target) (chan http.Request, error) {
	requestQueue := make(chan http.Request)
	//attempt to build one request - if passes, the rest should too
	_, err := buildRequest(target)
	if err != nil {
		return nil, errors.New("failed to create request with target configuration: " + err.Error())
	}
	go func() {
		for i := 0; i < count; i++ {
			req, err := buildRequest(target)
			if err != nil {
				//this shouldn't happen, but probably should handle for it
				continue
			}
			requestQueue <- req
		}
		close(requestQueue)
	}()
	return requestQueue, nil
}
