package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/joho/godotenv"
	"github.com/streadway/amqp"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
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

type Attachments struct {
	Name string `json:"name"`
	Src  string `json:"src"`
	Type string `json:"type"`
}

type sendMessageRequest struct {
	ConversationID int           `json:"conversationId"`
	Message        string        `json:"message"`
	Attachments    []Attachments `json:"attachments"`
}

type sendIdentifierRequest struct {
	ConversationId int `json:"conversationId"`
}

type FileUploadResponse struct {
	Status string `json:"status"`
	Data   struct {
		ExternalID int `json:"external_id"`
	} `json:"data"`
}

type SuccessSendMessage struct {
	Status string `json:"status"`
	Data   struct {
		ID        int `json:"id"`
		CompanyID int `json:"company_id"`
		Channel   struct {
			ID   int    `json:"id"`
			Type string `json:"type"`
		} `json:"channel"`
		ConversationID int         `json:"conversation_id"`
		State          string      `json:"state"`
		MessageID      interface{} `json:"message_id"`
		Details        interface{} `json:"details"`
		CreatedAt      int         `json:"created_at"`
	} `json:"data"`
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
		false,
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

			var attachments []int

			for _, value := range sendMessageRequest.Attachments {
				index := strings.Index(value.Src, ",")
				data, err := base64.StdEncoding.DecodeString(value.Src[index+1:])

				bodyBuf := &bytes.Buffer{}
				bodyWriter := multipart.NewWriter(bodyBuf)

				writer, err := bodyWriter.CreateFormFile("file", value.Name)

				failOnError(err, "Can`t create form file")
				_, err = io.Copy(writer, bytes.NewReader(data))
				failOnError(err, "Can`t copy form file")
				bodyWriter.Close()

				purl := fmt.Sprintf("https://api.pact.im/p1/companies/%s/conversations/%d/messages/attachments", os.Getenv("PACT_COMPANY_ID"), sendMessageRequest.ConversationID)
				req, err := http.NewRequest("POST", purl, bodyBuf)

				failOnError(err, "Can`t create send message request")

				req.Header.Set("Content-Type", bodyWriter.FormDataContentType())
				req.Header.Set("X-Private-Api-Token", os.Getenv("PACT_API_KEY"))

				resp, err := http.DefaultClient.Do(req)

				failOnError(err, "Can`t send message request")

				fileUploadResponse := FileUploadResponse{}
				err = json.NewDecoder(resp.Body).Decode(&fileUploadResponse)
				failOnError(err, "Can`t decode file response")

				log.Printf("Upload attachment: %s %d", fileUploadResponse.Status, fileUploadResponse.Data.ExternalID)

				attachments = append(attachments, fileUploadResponse.Data.ExternalID)

				resp.Body.Close()
			}

			data := url.Values{}

			for _, value := range attachments {
				data.Add("attachments_ids[]", strconv.Itoa(value))
			}

			data.Add("message", sendMessageRequest.Message)

			purl := fmt.Sprintf("https://api.pact.im/p1/companies/%s/conversations/%d/messages", os.Getenv("PACT_COMPANY_ID"), sendMessageRequest.ConversationID)
			req, err := http.NewRequest("POST", purl, bytes.NewBufferString(data.Encode()))
			failOnError(err, "Can`t create send message request")
			req.Header.Set("X-Private-Api-Token", os.Getenv("PACT_API_KEY"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resp, err := http.DefaultClient.Do(req)

			successSendMessage := SuccessSendMessage{}
			failOnError(err, "Can`t send message request")
			err = json.NewDecoder(resp.Body).Decode(&successSendMessage)
			failOnError(err, "Can`t decode message response")
			log.Printf("Send messge: %s %d %s", successSendMessage.Status, successSendMessage.Data.ID, successSendMessage.Data.State)

			resp.Body.Close()
			d.Ack(false)
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
