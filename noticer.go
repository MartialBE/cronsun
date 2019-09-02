package cronsun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"time"

	client "github.com/coreos/etcd/clientv3"
	"github.com/go-gomail/gomail"

	"github.com/royeo/dingrobot"
	"github.com/xiao5-neradigm/cronsun/conf"
	"github.com/xiao5-neradigm/cronsun/log"
	"github.com/xiao5-neradigm/cronsun/utils"
)

type Noticer interface {
	Serve()
	Send(*Message)
}

type Message struct {
	Subject string
	Body    string
	To      []string
}

type Mail struct {
	cf      *conf.MailConf
	open    bool
	sc      gomail.SendCloser
	timer   *time.Timer
	msgChan chan *Message
}

func NewMail(timeout time.Duration) (m *Mail, err error) {
	var (
		sc   gomail.SendCloser
		done = make(chan struct{})
		cf   = conf.Config.Mail
	)

	// qq 邮箱的 Auth 出错后， 501 命令超时 2min 才能退出
	go func() {
		sc, err = cf.Dialer.Dial()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		err = fmt.Errorf("connect to smtp timeout")
	}

	if err != nil {
		return
	}

	m = &Mail{
		cf:      cf,
		open:    true,
		sc:      sc,
		timer:   time.NewTimer(time.Duration(cf.Keepalive) * time.Second),
		msgChan: make(chan *Message, 8),
	}
	return
}

func (m *Mail) Serve() {
	var err error
	sm := gomail.NewMessage()
	for {
		select {
		case msg := <-m.msgChan:
			m.timer.Reset(time.Duration(m.cf.Keepalive) * time.Second)
			if !m.open {
				if m.sc, err = m.cf.Dialer.Dial(); err != nil {
					log.Warnf("smtp send msg[%+v] err: %s", msg, err.Error())
					continue
				}
				m.open = true
			}

			sm.Reset()
			sm.SetHeader("From", m.cf.Username)
			sm.SetHeader("To", msg.To...)
			sm.SetHeader("Subject", msg.Subject)
			sm.SetBody("text/plain", msg.Body)
			if err := gomail.Send(m.sc, sm); err != nil {
				log.Warnf("smtp send msg[%+v] err: %s", msg, err.Error())
			}
		case <-m.timer.C:
			if m.open {
				if err = m.sc.Close(); err != nil {
					log.Warnf("close smtp server err: %s", err.Error())
				}
				m.open = false
			}
			m.timer.Reset(time.Duration(m.cf.Keepalive) * time.Second)
		}
	}
}

func (m *Mail) Send(msg *Message) {
	m.msgChan <- msg
}

type HttpAPI struct{}

func (h *HttpAPI) Serve() {}

func (h *HttpAPI) Send(msg *Message) {
	body, err := json.Marshal(msg)
	if err != nil {
		log.Warnf("http api send msg[%+v] err: %s", msg, err.Error())
		return
	}

	req, err := http.NewRequest("POST", conf.Config.Mail.HttpAPI, bytes.NewBuffer(body))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warnf("http api send msg[%+v] err: %s", msg, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return
	}

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("http api send msg[%+v] err: %s", msg, err.Error())
		return
	}
	log.Warnf("http api send msg[%+v] err: %s", msg, string(data))
	return
}

type DingAPI struct{}

type DingMessage struct {
	msgType  string
	markdown struct {
		title string
		text  string
	}
	at struct {
		atMobiles []string
		isAtAll   bool
	}
}

type dingResponse struct {
	Errcode int
	Errmsg  string
}

func (h *DingAPI) Serve() {}

func (h *DingAPI) Send(msg *Message) {
	dingMSG := new(DingMessage)
	dingMSG.setAtArray(msg.To)
	dingMSG.setMarkdown(msg.Subject, msg.Body)

	robot := dingrobot.NewRobot(conf.Config.Mail.DingAPI)
	err := robot.SendMarkdown(dingMSG.markdown.title, dingMSG.markdown.text, dingMSG.at.atMobiles, dingMSG.at.isAtAll)
	if err != nil {
		log.Warnf("ding api send failed msg[%+v] err: %s", msg, err.Error())
		return
	}
	return
}

func (dingMSG *DingMessage) setAtArray(to []string) {
	for i, value := range to {
		if value != "de" {
			continue
		}
		to = append(to[:i], to[i+1:]...)
	}
	dingMSG.at.atMobiles = to
	dingMSG.at.isAtAll = false
}

func (dingMSG *DingMessage) setMarkdown(subject, body string) {
	dingMSG.msgType = "markdown"
	job := utils.Isset(regexp.MustCompile(`(?:job\[)(.*?)(?:\])`).FindStringSubmatch(subject), 1)
	node := utils.Isset(regexp.MustCompile(`(?:node\[)(.*?)(?:\])`).FindStringSubmatch(subject), 1)
	time := utils.Isset(regexp.MustCompile(`(?:time\[)(.*?)(?:\])`).FindStringSubmatch(subject), 1)

	dingMSG.markdown.title = job + "出错了"
	dingMSG.markdown.text = "### " + job + "出错了" + utils.Implode(" @", dingMSG.at.atMobiles) + "\n#### 任务名称：" + job + "\n#### 执行节点：" + node + "\n#### 错误时间：" + time + "\n#### 错误详情：\n```\n" + subject + "\n" + body + "\n```\n"
}

func StartNoticer(n Noticer) {
	go n.Serve()
	go monitorNodes(n)

	rch := DefalutClient.Watch(conf.Config.Noticer, client.WithPrefix())
	var err error
	for wresp := range rch {
		for _, ev := range wresp.Events {
			switch {
			case ev.IsCreate(), ev.IsModify():
				msg := new(Message)
				if err = json.Unmarshal(ev.Kv.Value, msg); err != nil {
					log.Warnf("msg[%s] umarshal err: %s", string(ev.Kv.Value), err.Error())
					continue
				}

				if len(conf.Config.Mail.To) > 0 {
					msg.To = append(msg.To, conf.Config.Mail.To...)
				}
				n.Send(msg)
			}
		}
	}
}

func monitorNodes(n Noticer) {
	rch := WatchNode()

	for wresp := range rch {
		for _, ev := range wresp.Events {
			switch {
			case ev.Type == client.EventTypeDelete:
				id := GetIDFromKey(string(ev.Kv.Key))
				log.Errorf("cronnode DELETE event detected, node UUID: %s", id)

				node, err := GetNodesByID(id)
				if err != nil {
					log.Warnf("failed to fetch node[%s] from mongodb: %s", id, err.Error())
					continue
				}

				if node.Alived {
					n.Send(&Message{
						Subject: fmt.Sprintf("[Cronsun Warning] Node[%s] break away cluster at %s",
							node.Hostname, time.Now().Format(time.RFC3339)),
						Body: fmt.Sprintf("Cronsun Node breaked away cluster, this might happened when node crash or network problems.\nUUID: %s\nHostname: %s\nIP: %s\n", id, node.Hostname, node.IP),
						To:   conf.Config.Mail.To,
					})
				}
			}
		}
	}
}
