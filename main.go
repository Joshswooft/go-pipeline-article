package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
)

func producer(ctx context.Context, strings []string) (<-chan string, error) {
	outChannel := make(chan string)

	// wrapping in a goroutine prevents deadlock
	go func() {
		// good strat here is whoever opens the channel should be in charge of closing
		// no risk of sending to a closed channel = panic!
		defer close(outChannel)

		for _, s := range strings {
			// tries to receive value from ctx if not done then moves to next branch
			// we use another case instead of 'default' statement because we'd be stuck blocking outChannel
			// 2nd case statement allows us to switch between trying the 2 cases.
			select {
			case <-ctx.Done():
				return
			case outChannel <- s:
			}
		}
	}()

	return outChannel, nil
}

func sink(ctx context.Context, cancelFunc context.CancelFunc, values <-chan string, errors <-chan error) {
	for {
		select {
		case <-ctx.Done():
			log.Print(ctx.Err().Error())
			return

		// if we receive an error then we stop the pipeline from running
		case err := <-errors:
			if err != nil {
				log.Println("error: ", err.Error())
				cancelFunc()
			}
		case val, ok := <-values:
			if ok {
				log.Printf("sink: %s", val)
			} else {
				log.Print("done")
				return
			}
		}
	}
}

func step[In any, Out any](
	ctx context.Context,
	inputChannel <-chan In,
	fn func(In) (Out, error),
) (chan Out, chan error) {
	outputChannel := make(chan Out)
	errorChannel := make(chan error)

	limit := int64(2)
	// Use all CPU cores to maximize efficiency. We'll set the limit to 2 so you
	// can see the values being processed in batches of 2 at a time, in parallel
	// limit := int64(runtime.NumCPU())
	sem1 := semaphore.NewWeighted(limit)

	go func() {
		defer close(outputChannel)
		defer close(errorChannel)

		for {
			select {
			case <-ctx.Done():
				break
			case s, ok := <-inputChannel:
				if ok {
					// defer doing the work until we have available resources
					if err := sem1.Acquire(ctx, 1); err != nil {
						log.Printf("Failed to acquire semaphore: %v", err)
						break
					}

					go func(s In) {
						// finished processing value
						defer sem1.Release(1)
						time.Sleep(time.Second * 3)

						result, err := fn(s)
						if err != nil {
							errorChannel <- err
						} else {
							outputChannel <- result
						}
					}(s)
				} else {
					if err := sem1.Acquire(ctx, limit); err != nil {
						log.Printf("Failed to acquire semaphore: %v", err)
					}
					return
				}
			}
		}
	}()

	return outputChannel, errorChannel
}

func Merge[T any](ctx context.Context, cs ...<-chan T) <-chan T {
	var wg sync.WaitGroup
	out := make(chan T)

	output := func(c <-chan T) {
		defer wg.Done()
		for n := range c {
			select {
			case out <- n:
			case <-ctx.Done():
				return
			}
		}
	}

	wg.Add(len(cs))
	for _, c := range cs {
		go output(c)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}

func transformA(s string) (string, error) {
	log.Println("transformA input: ", s)
	return strings.ToLower(s), nil
}

func transformB(s string) (string, error) {
	log.Println("transformB input: ", s)
	// Comment this out to see the pipeline finish successfully
	if s == "foo" {
		return "", errors.New("oh no")
	}

	return strings.Title(s), nil
}

func main() {
	source := []string{"FOO", "BAR", "BAX"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	readStream, err := producer(ctx, source)
	if err != nil {
		log.Fatal(err)
	}

	step1results, step1errors := step(ctx, readStream, transformA)
	step2results, step2errors := step(ctx, step1results, transformB)
	allErrors := Merge(ctx, step1errors, step2errors)

	sink(ctx, cancel, step2results, allErrors)
}
