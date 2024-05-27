package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"github.com/joho/godotenv"
	"golang.org/x/text/encoding/charmap"
)

// I won't change this, so I won't bother to load from .env
const batchSize = 100

func main() {

	// Register the ISO-8859-1 charset handler
	charset.RegisterEncoding("iso-8859-1", charmap.ISO8859_1)

	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	imapServer := os.Getenv("IMAP_SERVER")
	imapUsername := os.Getenv("IMAP_USERNAME")
	imapPassword := os.Getenv("IMAP_PASSWORD")
	imapFolder := os.Getenv("IMAP_FOLDER")
	destDir := os.Getenv("DESTINATION_DIR")
	processedUIDsFile := path.Join(destDir, "processed_uids.txt")

	// Load processed UIDs
	processedUIDs := loadProcessedUIDs(processedUIDsFile)

	// Connect to server
	c, err := client.DialTLS(imapServer, nil)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Connected")

	// Login
	if err := c.Login(imapUsername, imapPassword); err != nil {
		log.Fatal(err)
	}
	log.Printf("Logged in to IMAP server: %s\n", imapServer)

	// Select mailbox
	mbox, err := c.Select(imapFolder, false)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Mailbox selected %s, total messages: %d\n", imapFolder, mbox.Messages)

	if mbox.Messages == 0 {
		log.Println("No messages in mailbox")
		return
	}

	// Create destination directory if it doesn't exist
	if _, err := os.Stat(destDir); os.IsNotExist(err) {
		os.Mkdir(destDir, os.ModePerm)
	}

	// Fetch messages in batches
	for i := uint32(1); i <= mbox.Messages; i += batchSize {
		end := i + batchSize - 1
		if end > mbox.Messages {
			end = mbox.Messages
		}

		seqset := new(imap.SeqSet)
		seqset.AddRange(i, end)

		log.Printf("Fetching messages %d:%d\n", i, i+batchSize-1)
		messages := make(chan *imap.Message, batchSize)
		done := make(chan error, 1)
		go func() {
			// done <- c.UidFetch(seqset, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchBodyStructure}, messages)
			done <- c.Fetch(seqset, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchBodyStructure}, messages)
		}()

		log.Printf("Processing %d messages\n", len(messages))

		// Process each message
		for msg := range messages {
			if _, processed := processedUIDs[msg.Uid]; processed {
				log.Printf("Message UID: %d already processed, skipping\n", msg.Uid)
				continue
			}

			log.Printf("Processing message UID: %d\n", msg.Uid)
			processMessage(c, msg, destDir)
			processedUIDs[msg.Uid] = struct{}{}
			appendProcessedUID(processedUIDsFile, msg.Uid)
		}

		if err := <-done; err != nil {
			log.Fatal(err)
		}
		log.Println("Batch processed")
	}

	// Logout
	if err := c.Logout(); err != nil {
		log.Fatal(err)
	}
	log.Println("Logged out")
}

func processMessage(c *client.Client, msg *imap.Message, destDir string) {
	section := &imap.BodySectionName{}
	seqset := new(imap.SeqSet)
	seqset.AddNum(msg.Uid)

	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqset, []imap.FetchItem{section.FetchItem()}, messages)
	}()

	for msg := range messages {
		r := msg.GetBody(section)
		if r == nil {
			log.Fatal("Server didn't return message body")
		}

		mr, err := mail.CreateReader(r)
		if err != nil {
			log.Fatal(err)
		}

		// Process each message's parts
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}

			switch h := part.Header.(type) {
			case *mail.AttachmentHeader:
				filename, _ := h.Filename()
				fullPath := filepath.Join(destDir, filename)

				if _, err := os.Stat(fullPath); err == nil {
					timestamp := time.Now().Format("20060102_150405")
					fullPath = filepath.Join(destDir, fmt.Sprintf("%s_%s", timestamp, filename))
				}

				log.Println("Saving attachment to:", fullPath)

				file, err := os.Create(fullPath)
				if err != nil {
					continue
				}
				defer file.Close()

				// write part.Body to file
				_, err = io.Copy(file, part.Body)
				if err != nil {
					log.Fatal(err)
				}
			}
		}
	}

	if err := <-done; err != nil {
		log.Fatal(err)
	}
}

func loadProcessedUIDs(filename string) map[uint32]struct{} {
	processedUIDs := make(map[uint32]struct{})
	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return processedUIDs
		}
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		uid, err := strconv.ParseUint(scanner.Text(), 10, 32)
		if err != nil {
			log.Fatal(err)
		}
		processedUIDs[uint32(uid)] = struct{}{}
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	return processedUIDs
}

func appendProcessedUID(filename string, uid uint32) {
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	if _, err := file.WriteString(fmt.Sprintf("%d\n", uid)); err != nil {
		log.Fatal(err)
	}
}
