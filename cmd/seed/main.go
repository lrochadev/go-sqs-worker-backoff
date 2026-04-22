package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/joho/godotenv"
)

var planets = []string{
	`{"name":"Tatooine","climate":"arid","terrain":"desert"}`,
	`{"name":"Alderaan","climate":"temperate","terrain":"grasslands"}`,
	`{"name":"Hoth","climate":"frozen","terrain":"tundra"}`,
	`{"name":"Dagobah","climate":"murky","terrain":"swamp"}`,
	`{"name":"Endor","climate":"temperate","terrain":"forest"}`,
	`{"name":"Naboo","climate":"temperate","terrain":"grassy hills"}`,
	`{"name":"Coruscant","climate":"temperate","terrain":"cityscape"}`,
	`{"name":"Bespin","climate":"temperate","terrain":"gas giant"}`,
	`{"name":"Kamino","climate":"stormy","terrain":"ocean"}`,
	`{"name":"Geonosis","climate":"arid","terrain":"rock"}`,
	`{"name":"Mustafar","climate":"hot","terrain":"volcanic"}`,
	`{"name":"Kashyyyk","climate":"tropical","terrain":"forest"}`,
	`{"name":"Utapau","climate":"arid","terrain":"sinkholes"}`,
	`{"name":"Mygeeto","climate":"frigid","terrain":"crystalline"}`,
	`{"name":"Felucia","climate":"hot","terrain":"fungi forest"}`,
	`{"name":"Cato Neimoidia","climate":"temperate","terrain":"rock arches"}`,
	`{"name":"Saleucami","climate":"hot","terrain":"caves"}`,
	`{"name":"Jakku","climate":"arid","terrain":"desert"}`,
	`{"name":"Crait","climate":"arid","terrain":"salt flats"}`,
	`{"name":"Exegol","climate":"stormy","terrain":"ruins"}`,
}

func main() {
	_ = godotenv.Load()

	total := flag.Int("total", 500_000, "total messages to publish")
	workers := flag.Int("workers", 64, "concurrent senders")
	flag.Parse()

	queueURL := os.Getenv("SQS_QUEUE_URL")
	region := os.Getenv("AWS_REGION")
	endpoint := os.Getenv("SQS_ENDPOINT")
	if queueURL == "" || region == "" {
		log.Fatalf("SQS_QUEUE_URL and AWS_REGION are required")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if endpoint != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		log.Fatalf("aws config: %v", err)
	}

	var clientOpts []func(*sqs.Options)
	if endpoint != "" {
		ep := endpoint
		clientOpts = append(clientOpts, func(o *sqs.Options) { o.BaseEndpoint = &ep })
	}
	client := sqs.NewFromConfig(awsCfg, clientOpts...)

	const batchSize = 10
	batchCount := (*total + batchSize - 1) / batchSize
	batches := make(chan int, *workers*2)

	var sent atomic.Int64
	var failed atomic.Int64
	start := time.Now()

	// progress reporter
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				elapsed := time.Since(start).Seconds()
				s := sent.Load()
				fmt.Printf("[seed] sent=%d/%d failed=%d elapsed=%.1fs rate=%.0f msg/s\n",
					s, *total, failed.Load(), elapsed, float64(s)/elapsed)
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			entries := make([]types.SendMessageBatchRequestEntry, batchSize)
			for batchIdx := range batches {
				baseSeq := batchIdx * batchSize
				remaining := *total - baseSeq
				n := batchSize
				if remaining < n {
					n = remaining
				}
				for i := 0; i < n; i++ {
					seq := baseSeq + i
					body := planets[seq%len(planets)]
					idStr := fmt.Sprintf("%d-%d", id, seq)
					entries[i] = types.SendMessageBatchRequestEntry{
						Id:          &idStr,
						MessageBody: &body,
					}
				}
				out, err := client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
					QueueUrl: &queueURL,
					Entries:  entries[:n],
				})
				if err != nil {
					failed.Add(int64(n))
					continue
				}
				sent.Add(int64(len(out.Successful)))
				failed.Add(int64(len(out.Failed)))
			}
		}(w)
	}

	for i := 0; i < batchCount; i++ {
		select {
		case batches <- i:
		case <-ctx.Done():
			break
		}
	}
	close(batches)
	wg.Wait()
	close(done)

	elapsed := time.Since(start)
	s := sent.Load()
	fmt.Printf("\n=== seed complete ===\n")
	fmt.Printf("sent:    %d\n", s)
	fmt.Printf("failed:  %d\n", failed.Load())
	fmt.Printf("elapsed: %s\n", elapsed)
	fmt.Printf("rate:    %.0f msg/s\n", float64(s)/elapsed.Seconds())
}
