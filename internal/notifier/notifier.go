package notifier

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
)

type Payload struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// SendNotification 发送统一通知
func SendNotification(apiEndpoint, title, content string) error {
	payload := Payload{
		Title:   title,
		Content: content,
	}

	data, _ := json.Marshal(payload)
	resp, err := http.Post(apiEndpoint, "application/json", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("推送返回状态码: %d", resp.StatusCode)
	}
	return nil
}
