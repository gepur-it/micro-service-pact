package main

import (
	"encoding/json"
	"fmt"
	"github.com/joho/godotenv"
	"github.com/streadway/amqp"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

var AMQPConnection *amqp.Connection
var AMQPChannel *amqp.Channel

type callbackRequest struct {
	Type  string                 `json:"type"`
	Event string                 `json:"event"`
	Data  map[string]interface{} `json:"data"`
}

type sendMessageRequest struct {
	ConversationId int    `json:"conversationId"`
	Message        string `json:"message"`
}

type sendIdentifierRequest struct {
	ConversationId int `json:"conversationId"`
}

type receiveIdentiferResponse struct {
	Status string `json:"status"`
	Data   struct {
		Conversation struct {
			ExternalID       int    `json:"external_id"`
			Name             string `json:"name"`
			ChannelID        int    `json:"channel_id"`
			ChannelType      string `json:"channel_type"`
			CreatedAt        string `json:"created_at"`
			Avatar           string `json:"avatar"`
			SenderExternalID string `json:"sender_external_id"`
			Meta             struct {
			} `json:"meta"`
		} `json:"conversation"`
	} `json:"data"`
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
		panic(fmt.Sprintf("%s: %s", msg, err))
	}
}

func receiver(w http.ResponseWriter, r *http.Request) {
	callbackRequest := callbackRequest{}

	err := json.NewDecoder(r.Body).Decode(&callbackRequest)
	failOnError(err, "Can`t decode webHook callBack")

	callbackRequestJson, err := json.Marshal(callbackRequest)
	failOnError(err, "Can`t serialise webHook response")

	log.Printf("Received a callback: %s", callbackRequestJson)

	name := fmt.Sprintf("pact_receive_callback")

	query, err := AMQPChannel.QueueDeclare(
		name,
		true,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to declare a queue")

	err = AMQPChannel.Publish(
		"",
		query.Name,
		false,
		false,
		amqp.Publishing{
			DeliveryMode: amqp.Transient,
			ContentType:  "application/json",
			Body:         callbackRequestJson,
			Timestamp:    time.Now(),
		})

	failOnError(err, "Failed to publish a message")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(callbackRequestJson)
}

func identifier() {
	query, err := AMQPChannel.QueueDeclare(
		"erp_send_identifier",
		true,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to declare a queue")

	msgs, err := AMQPChannel.Consume(
		query.Name,
		"",
		true,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to register a consumer")

	forever := make(chan bool)

	go func() {
		for d := range msgs {
			sendIdentifierRequest := sendIdentifierRequest{}
			err := json.Unmarshal(d.Body, &sendIdentifierRequest)
			failOnError(err, "Can`t decode sender callBack")

			url := fmt.Sprintf("https://api.pact.im/p1/companies/%s/conversations/%d", os.Getenv("PACT_COMPANY_ID"), sendIdentifierRequest.ConversationId)
			req, err := http.NewRequest("GET", url, nil)
			failOnError(err, "Can`t create identifier request")

			req.Header.Set("X-Private-Api-Token", os.Getenv("PACT_API_KEY"))

			resp, err := http.DefaultClient.Do(req)
			failOnError(err, "Can`t send identifier request")

			log.Printf("Send a identifier request: %d", sendIdentifierRequest.ConversationId)

			receiveIdentiferResponse := receiveIdentiferResponse{}
			json.NewDecoder(resp.Body).Decode(&receiveIdentiferResponse)
			failOnError(err, "Can`t decode webHook callBack")

			testCallbackRequestJson, err := json.Marshal(receiveIdentiferResponse)
			failOnError(err, "Can`t serialise webHook response")

			log.Printf("Received a response: %s", testCallbackRequestJson)

			var inInterface map[string]interface{}
			b, err := json.Marshal(receiveIdentiferResponse.Data.Conversation)
			failOnError(err, "Can`t serialise webHook response")
			json.Unmarshal(b, &inInterface)

			identifierCallbackRequest := callbackRequest{
				Type:  "account",
				Event: "update",
				Data:  inInterface,
			}

			fakeCallbackRequestJson, err := json.Marshal(identifierCallbackRequest)
			failOnError(err, "Can`t serialise webHook response")

			name := fmt.Sprintf("pact_receive_callback")

			query, err := AMQPChannel.QueueDeclare(
				name,
				true,
				false,
				false,
				false,
				nil,
			)
			failOnError(err, "Failed to declare a queue")

			err = AMQPChannel.Publish(
				"",
				query.Name,
				false,
				false,
				amqp.Publishing{
					DeliveryMode: amqp.Transient,
					ContentType:  "application/json",
					Body:         fakeCallbackRequestJson,
					Timestamp:    time.Now(),
				})

			failOnError(err, "Failed to publish a message")

			log.Printf("Send fake callback: %s", fakeCallbackRequestJson)
			resp.Body.Close()
		}
	}()

	<-forever
}

func sender() {
	query, err := AMQPChannel.QueueDeclare(
		"erp_send_message",
		true,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to declare a queue")

	msgs, err := AMQPChannel.Consume(
		query.Name,
		"",
		true,
		false,
		false,
		false,
		nil,
	)
	failOnError(err, "Failed to register a consumer")

	forever := make(chan bool)

	go func() {
		for d := range msgs {
			sendMessageRequest := sendMessageRequest{}
			err := json.Unmarshal(d.Body, &sendMessageRequest)
			failOnError(err, "Can`t decode sender callBack")

			// Generated by curl-to-Go: https://mholt.github.io/curl-to-go

			body := strings.NewReader(fmt.Sprintf("message=%s", sendMessageRequest.Message))

			url := fmt.Sprintf("https://api.pact.im/p1/companies/%s/conversations/%d/messages", os.Getenv("PACT_COMPANY_ID"), sendMessageRequest.ConversationId)
			req, err := http.NewRequest("POST", url, body)

			failOnError(err, "Can`t create send message request")

			req.Header.Set("X-Private-Api-Token", os.Getenv("PACT_API_KEY"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			resp, err := http.DefaultClient.Do(req)
			failOnError(err, "Can`t send message request")

			log.Printf("Send a message: %d %s", sendMessageRequest.ConversationId, sendMessageRequest.Message)

			resp.Body.Close()
		}
	}()

	<-forever
}

func init() {
	err := godotenv.Load()
	failOnError(err, "Error loading .env file")

	cs := fmt.Sprintf("amqp://%s:%s@%s:%s/%s",
		os.Getenv("RABBITMQ_ERP_LOGIN"),
		os.Getenv("RABBITMQ_ERP_PASS"),
		os.Getenv("RABBITMQ_ERP_HOST"),
		os.Getenv("RABBITMQ_ERP_PORT"),
		os.Getenv("RABBITMQ_ERP_VHOST"))

	connection, err := amqp.Dial(cs)
	failOnError(err, "Failed to connect to RabbitMQ")
	AMQPConnection = connection
	//defer connection.Close()

	channel, err := AMQPConnection.Channel()
	failOnError(err, "Failed to open a channel")
	AMQPChannel = channel

	failOnError(err, "Failed to declare a queue")
}

func main() {
	http.HandleFunc("/", receiver)

	go sender()
	go identifier()

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", os.Getenv("PACT_LISTEN_PORT")), nil))
	defer AMQPConnection.Close()
	defer AMQPChannel.Close()
}
