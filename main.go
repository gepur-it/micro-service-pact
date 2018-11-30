package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"github.com/zbindenren/logrus_mail"
	"io"
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
var logger = logrus.New()

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
		logger.WithFields(logrus.Fields{
			"error": err,
		}).Fatal(msg)
	}
}

func getenvInt(key string) (int, error) {
	s := os.Getenv(key)
	v, err := strconv.Atoi(s)

	if err != nil {
		return 0, err
	}

	return v, nil
}

func receiver(w http.ResponseWriter, r *http.Request) {
	callbackRequest := callbackRequest{}

	err := json.NewDecoder(r.Body).Decode(&callbackRequest)
	failOnError(err, "Can`t decode webHook callBack")

	callbackRequestJson, err := json.Marshal(callbackRequest)
	failOnError(err, "Can`t serialise webHook response")

	logger.WithFields(logrus.Fields{
		"data": callbackRequestJson,
	}).Info("Received a callback:")

	err = AMQPChannel.Publish(
		"",
		"pact_receive_callback",
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
	msgs, err := AMQPChannel.Consume(
		"erp_send_identifier",
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
			sendIdentifierRequest := sendIdentifierRequest{}
			err := json.Unmarshal(d.Body, &sendIdentifierRequest)
			failOnError(err, "Can`t decode sender callBack")

			identifierUrl := fmt.Sprintf("https://api.pact.im/p1/companies/%s/conversations/%d", os.Getenv("PACT_COMPANY_ID"), sendIdentifierRequest.ConversationId)
			req, err := http.NewRequest("GET", identifierUrl, nil)
			failOnError(err, "Can`t create identifier request")

			req.Header.Set("X-Private-Api-Token", os.Getenv("PACT_API_KEY"))

			resp, err := http.DefaultClient.Do(req)
			failOnError(err, "Can`t send identifier request")

			logger.WithFields(logrus.Fields{
				"data": sendIdentifierRequest,
			}).Info("Send a identifier request:")

			receiveIdentiferResponse := receiveIdentiferResponse{}
			json.NewDecoder(resp.Body).Decode(&receiveIdentiferResponse)
			failOnError(err, "Can`t decode webHook callBack")

			logger.WithFields(logrus.Fields{
				"data": receiveIdentiferResponse,
			}).Info("Received a identifier response:")

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

			err = AMQPChannel.Publish(
				"",
				"pact_receive_callback",
				false,
				false,
				amqp.Publishing{
					DeliveryMode: amqp.Transient,
					ContentType:  "application/json",
					Body:         fakeCallbackRequestJson,
					Timestamp:    time.Now(),
				})

			failOnError(err, "Failed to publish a message")

			logger.WithFields(logrus.Fields{
				"data": fakeCallbackRequestJson,
			}).Info("Send fake identifier callback:")

			resp.Body.Close()
			d.Ack(false)
		}
	}()

	<-forever
}

func sender() {
	msgs, err := AMQPChannel.Consume(
		"erp_send_message",
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

				logger.WithFields(logrus.Fields{
					"data": fileUploadResponse,
				}).Info("Upload attachment:")

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

			logger.WithFields(logrus.Fields{
				"data": successSendMessage,
			}).Info("Send message: ")

			resp.Body.Close()
			d.Ack(false)
		}
	}()

	<-forever
}

func init() {
	err := godotenv.Load()
	failOnError(err, "Error loading .env file")

	port, err := getenvInt("LOGTOEMAIL_SMTP_PORT")

	if err != nil {
		panic(fmt.Sprintf("%s: %s", "Error read smtp port from env", err))
	}

	hook, err := logrus_mail.NewMailAuthHook(
		os.Getenv("LOGTOEMAIL_APP_NAME"),
		os.Getenv("LOGTOEMAIL_SMTP_HOST"),
		port,
		os.Getenv("LOGTOEMAIL_SMTP_FROM"),
		os.Getenv("LOGTOEMAIL_SMTP_TO"),
		os.Getenv("LOGTOEMAIL_SMTP_USERNAME"),
		os.Getenv("LOGTOEMAIL_SMTP_PASSWORD"),
	)

	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(os.Stdout)
	logger.SetFormatter(&logrus.TextFormatter{})

	logger.Hooks.Add(hook)

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
	logger.WithFields(logrus.Fields{}).Info("Server init:")
}

func main() {
	logger.WithFields(logrus.Fields{}).Info("Server starting:")
	http.HandleFunc("/", receiver)

	go sender()
	go identifier()

	logger.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", os.Getenv("PACT_LISTEN_PORT")), nil))
	defer AMQPConnection.Close()
	defer AMQPChannel.Close()
	logger.WithFields(logrus.Fields{}).Info("Server stopped:")
}
