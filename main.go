package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type Planet struct {
	Name    string `json:"name"`
	Climate string `json:"climate"`
	Terrain string `json:"terrain"`
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Create SQS client
	client := sqs.NewFromConfig(aws.Config{
		Region:       "us-east-1",
		BaseEndpoint: aws.String("http://localhost:4566"),
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
	})

	queueURL := "http://localhost:4566/000000000000/planet-queue"

	log.Println("Starting SQS consumer for planet-queue...")

	// Start consumer in a goroutine
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				receiveMessages(ctx, client, queueURL)
			}
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	log.Println("\nShutting down gracefully...")
	cancel()
	time.Sleep(1 * time.Second)
	log.Println("Consumer stopped")
}

func receiveMessages(ctx context.Context, client *sqs.Client, queueURL string) {
	result, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(queueURL),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     20,
	})

	if err != nil {
		log.Printf("Error receiving messages: %v\n", err)
		return
	}

	for _, message := range result.Messages {
		var planet Planet
		if err := json.Unmarshal([]byte(*message.Body), &planet); err != nil {
			log.Printf("Error unmarshaling message: %v\n", err)
			continue
		}

		// Log the planet payload as structured JSON
		planetJSON, _ := json.MarshalIndent(planet, "", "  ")
		log.Printf("Received planet: %s\n", string(planetJSON))

		// Delete message after processing
		_, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
			QueueUrl:      aws.String(queueURL),
			ReceiptHandle: message.ReceiptHandle,
		})

		if err != nil {
			log.Printf("Error deleting message: %v\n", err)
		} else {
			log.Printf("Message deleted: %s\n", *message.MessageId)
		}
	}
}
