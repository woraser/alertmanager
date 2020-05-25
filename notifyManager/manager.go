package notifyManager

import (
	"context"
	"fmt"
	"github.com/go-kit/kit/log"
	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/notify"
	"github.com/prometheus/alertmanager/notify/email"
	"github.com/prometheus/alertmanager/notify/hipchat"
	"github.com/prometheus/alertmanager/notify/opsgenie"
	"github.com/prometheus/alertmanager/notify/pagerduty"
	"github.com/prometheus/alertmanager/notify/pushover"
	"github.com/prometheus/alertmanager/notify/slack"
	"github.com/prometheus/alertmanager/notify/victorops"
	"github.com/prometheus/alertmanager/notify/webhook"
	"github.com/prometheus/alertmanager/notify/wechat"
	"github.com/prometheus/alertmanager/template"
	"github.com/prometheus/alertmanager/types"
	"sync"
	"time"
)

type Manager struct {
	stages notify.RoutingStage
	receivers []string
	tmpl *template.Template
	mtx sync.RWMutex
	InputChan chan *config.Receiver
	RemoveChan chan string
	stop chan struct{}
	logger   log.Logger
	wait func() time.Duration
	notificationLog notify.NotificationLog
	pb *notify.PipelineBuilder
}

func NewManager() *Manager {
	return &Manager{
		stages: nil,
		receivers: make([]string,0),
		tmpl: nil,
		InputChan: make(chan *config.Receiver),
		RemoveChan: make(chan string),
		stop: make(chan struct{}),
		logger: nil,
	}
}

func (m *Manager) ApplyConfig(
	logger log.Logger,
	tmpl *template.Template,
	receivers map[string][]notify.Integration,
	wait func() time.Duration,
	notificationLog notify.NotificationLog,
	pb *notify.PipelineBuilder) error {
	rs := make(notify.RoutingStage, len(receivers))

	for name := range receivers {
		m.receivers = append(m.receivers, name)
		st := notify.CreateReceiverStage(name, receivers[name], wait, notificationLog, pb.GetMetric())
		rs[name] = notify.MultiStage{st}
	}
	m.stages = rs
	m.tmpl = tmpl
	m.logger = logger
	m.wait = wait
	m.notificationLog = notificationLog
	m.pb = pb
	return nil
}

func (m *Manager) GetAllReceivers() []string{
	return m.receivers
}

func (m *Manager) VerifyName(name string) bool{
	if _,ok :=m.stages[name];ok {
		return false
	}
	return true
}

func (m *Manager) Exec(ctx context.Context, l log.Logger, alerts ...*types.Alert) (context.Context, []*types.Alert, error) {
	receiver, ok := notify.ReceiverName(ctx)
	if !ok {
		return ctx, nil, fmt.Errorf("receiver missing")
	}
	m.mtx.RLock()
	s, ok := m.stages[receiver]
	m.mtx.RUnlock()
	if !ok {
		return ctx, nil, fmt.Errorf("stage for receiver missing")
	}

	return s.Exec(ctx, l, alerts...)
}


func (m *Manager) Run() error {
	for {
		select {
		case r :=<-m.InputChan:
			m.append(r)
		case name := <-m.RemoveChan:
			m.remove(name)
		case <-m.stop:
			return nil
		}
	}
	return nil
}


func (m *Manager) Stop() error {
	return nil
}

func (m *Manager) remove(name string) {
	m.mtx.Lock()
	for i, value := range m.receivers {
		if value == name {
			m.receivers = append(m.receivers[:i], m.receivers[i+1:]...)
			break
		}
	}
	delete(m.stages, name)
	m.mtx.Unlock()
}

func (m *Manager) append(rcv *config.Receiver) {
	integrations, err := buildReceiverIntegrations(rcv, m.tmpl, m.logger)
	if err != nil {
		m.logger.Log("err","create receiver failed", err)
	}

	st := notify.CreateReceiverStage(rcv.Name, integrations, m.wait, m.notificationLog, m.pb.GetMetric())
	m.mtx.Lock()
	m.receivers = append(m.receivers, rcv.Name)
	m.stages[rcv.Name] = st
	m.mtx.Unlock()
}

func buildReceiverIntegrations(nc *config.Receiver, tmpl *template.Template, logger log.Logger) ([]notify.Integration, error) {
	var (
		errs         types.MultiError
		integrations []notify.Integration
		add          = func(name string, i int, rs notify.ResolvedSender, f func(l log.Logger) (notify.Notifier, error)) {
			n, err := f(log.With(logger, "integration", name))
			if err != nil {
				errs.Add(err)
				return
			}
			integrations = append(integrations, notify.NewIntegration(n, rs, name, i))
		}
	)

	for i, c := range nc.WebhookConfigs {
		add("webhook", i, c, func(l log.Logger) (notify.Notifier, error) { return webhook.New(c, tmpl, l) })
	}
	for i, c := range nc.EmailConfigs {
		add("email", i, c, func(l log.Logger) (notify.Notifier, error) { return email.New(c, tmpl, l), nil })
	}
	for i, c := range nc.PagerdutyConfigs {
		add("pagerduty", i, c, func(l log.Logger) (notify.Notifier, error) { return pagerduty.New(c, tmpl, l) })
	}
	for i, c := range nc.OpsGenieConfigs {
		add("opsgenie", i, c, func(l log.Logger) (notify.Notifier, error) { return opsgenie.New(c, tmpl, l) })
	}
	for i, c := range nc.WechatConfigs {
		add("wechat", i, c, func(l log.Logger) (notify.Notifier, error) { return wechat.New(c, tmpl, l) })
	}
	for i, c := range nc.SlackConfigs {
		add("slack", i, c, func(l log.Logger) (notify.Notifier, error) { return slack.New(c, tmpl, l) })
	}
	for i, c := range nc.HipchatConfigs {
		add("hipchat", i, c, func(l log.Logger) (notify.Notifier, error) { return hipchat.New(c, tmpl, l) })
	}
	for i, c := range nc.VictorOpsConfigs {
		add("victorops", i, c, func(l log.Logger) (notify.Notifier, error) { return victorops.New(c, tmpl, l) })
	}
	for i, c := range nc.PushoverConfigs {
		add("pushover", i, c, func(l log.Logger) (notify.Notifier, error) { return pushover.New(c, tmpl, l) })
	}
	if errs.Len() > 0 {
		return nil, &errs
	}
	return integrations, nil
}

