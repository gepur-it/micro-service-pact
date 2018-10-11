FROM golang:latest
WORKDIR /app
COPY . /app
RUN go get github.com/joho/godotenv
RUN go get github.com/streadway/amqp
CMD ["go", "run", "main.go", "&"]
EXPOSE 80

