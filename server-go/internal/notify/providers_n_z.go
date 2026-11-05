package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

func init() {
	for name, send := range map[string]provider{
		"notifery": notifery, "notifyapp": notifyApp, "ntfy": ntfy, "octopush": octopush, "OneBot": oneBot, "OneChat": oneChat,
		"Onesender": oneSender, "Ooredoo": ooredoo, "openwa": openWA, "Opsgenie": opsgenie, "PagerDuty": pagerDuty, "PagerTree": pagerTree,
		"pinglet": pinglet, "plivo": plivo, "promosms": promoSMS, "pumble": pumble, "pushbullet": pushbullet, "PushDeer": pushDeer,
		"pushover": pushover, "PushPlus": pushPlus, "pushy": pushy, "Resend": resend, "rocket.chat": rocketChat, "SendGrid": sendGrid,
		"ServerChan": serverChan, "serwersms": serwerSMS, "SevenIO": sevenIO, "signal": signal, "signalgrid": signalgrid, "SIGNL4": signl4,
		"slack": slack, "SMSGateway": smsGateway, "SMSPlanet": smsPlanet, "smsc": smsc, "SMSEagle": smsEagle, "smsir": smsIR,
		"SMSManager": smsManager, "SMSPartner": smsPartner, "smtp": sendSMTP, "Splunk": splunk, "SpugPush": spugPush, "squadcast": squadcast,
		"stackfield": stackfield, "teams": teams, "PushByTechulus": techulus, "telegram": telegram, "telnyx": telnyx, "Teltonika": teltonika,
		"threema": threema, "TurboSMTP": turboSMTP, "twilio": twilio, "VK": vk, "VKTeams": vkTeams, "waha": waha, "webhook": webhook,
		"WeCom": weCom, "whapi": whapi, "WPush": wPush, "WxPusher": wxPusher, "YZJ": yzj, "ZohoCliq": zohoCliq, "apprise": apprise,
	} {
		providers[name] = send
	}
}

func notifery(c *call) error {
	data := M{"title": c.or("notiferyTitle", "Uptime Kuma Alert"), "message": c.msg}
	if c.t("notiferyGroup") {
		data["group"] = c.cfg["notiferyGroup"]
	}
	if c.env.BaseURL != "" && c.mon != nil {
		data["message"] = c.msg + "\n\nMonitor: " + c.monitorURL()
	}
	if c.hasBeat() {
		data["code"] = 1
		if c.status() == statusUp {
			data["code"] = 0
		}
		if truthy(c.beat["ping"]) {
			data["duration"] = c.beat["ping"]
		}
	}
	return c.postOnly("https://api.notifery.com/event", data, H{"Content-Type": "application/json", "x-api-key": c.s("notiferyApiKey")})
}

func notifyApp(c *call) error {
	title := "Uptime Kuma"
	if c.mon != nil {
		title = stringOr(c.mon["name"], title)
	}
	if c.hasBeat() {
		if c.status() == statusUp {
			title += " is UP"
		} else {
			title += " is DOWN"
		}
	}
	body := M{"text": c.msg, "title": title}
	if c.t("notifyAppIconUrl") {
		body["iconUrl"] = c.cfg["notifyAppIconUrl"]
	}
	if c.isDown() {
		body["timeSensitive"] = true
	}
	return c.postOnly("https://push.getnotifyapp.com/notify-json/"+encodeURIComponent(c.s("notifyAppDeviceId"))+"?token="+encodeURIComponent(c.s("notifyAppToken")), body, nil)
}

func ntfy(c *call) error {
	headers := H{}
	switch c.s("ntfyAuthenticationMethod") {
	case "usernamePassword":
		headers["Authorization"] = basicAuth(c.s("ntfyusername"), c.s("ntfypassword"))
	case "accessToken":
		headers["Authorization"] = "Bearer " + c.s("ntfyaccesstoken")
	}
	if c.t("ntfyCall") {
		headers["X-Call"] = c.s("ntfyCall")
	}
	custom := func(title, message string) (string, string, error) {
		if !c.t("ntfyUseTemplate") {
			return title, message, nil
		}
		var err error
		if text := strings.TrimSpace(c.s("ntfyCustomTitle")); text != "" {
			if title, err = c.render(text); err != nil {
				return "", "", err
			}
		}
		if text := strings.TrimSpace(c.s("ntfyCustomMessage")); text != "" {
			if message, err = c.render(text); err != nil {
				return "", "", err
			}
		}
		return title, message, nil
	}
	target := c.s("ntfyserverurl")
	if !c.hasBeat() {
		title, message, err := custom(c.s("ntfytopic")+" [Uptime-Kuma]", c.msg)
		if err != nil {
			return err
		}
		return c.postOnly(target, M{"topic": c.v("ntfytopic"), "title": title, "message": message, "priority": c.v("ntfyPriority"), "tags": []any{"test_tube"}}, headers)
	}
	tags, status := []any{}, "unknown"
	priority := c.or("ntfyPriority", 4)
	switch c.status() {
	case statusDown:
		tags, status = []any{"red_circle"}, "Down"
		if c.t("ntfyPriorityDown") {
			priority = c.cfg["ntfyPriorityDown"]
		} else if n, ok := number(priority); ok {
			if n != 5 {
				priority = n + 1
			}
		} else {
			priority = js(priority) + "1"
		}
	case statusUp:
		tags, status = []any{"green_circle"}, "Up"
	}
	if list, ok := c.mon["tags"].([]any); ok {
		for _, tag := range list {
			entry, _ := tag.(M)
			if truthy(entry["value"]) {
				tags = append(tags, js(entry["name"])+": "+js(entry["value"]))
			} else {
				tags = append(tags, entry["name"])
			}
		}
	}
	title, message, err := custom(c.ms("name")+" "+status+" [Uptime-Kuma]", c.bs("msg"))
	if err != nil {
		return err
	}
	data := M{"topic": c.v("ntfytopic"), "message": message, "priority": priority, "title": title, "tags": tags}
	if u := c.mon["url"]; truthy(u) && u != "https://" {
		data["actions"] = []any{M{"action": "view", "label": "Open " + c.ms("name"), "url": u}}
	}
	if c.t("ntfyIcon") {
		data["icon"] = c.cfg["ntfyIcon"]
	}
	return c.postOnly(target, data, headers)
}

func octopush(c *call) error {
	version := c.s("octopushVersion")
	if version == "2" || version == "" {
		return c.postOnly("https://api.octopush.com/v1/public/sms-campaign/send", M{
			"recipients": []any{M{"phone_number": c.v("octopushPhoneNumber")}}, "text": ascii(c.msg), "type": c.v("octopushSMSType"),
			"purpose": "alert", "sender": c.v("octopushSenderName"),
		}, H{"api-key": c.s("octopushAPIKey"), "api-login": c.s("octopushLogin"), "cache-control": "no-cache"})
	}
	if version != "1" {
		return errors.New("unknown Octopush version")
	}
	kind := "XXX"
	if c.s("octopushDMSMSType") == "sms_premium" {
		kind = "FR"
	}
	values := url.Values{}
	for key, value := range map[string]string{
		"user_login": c.s("octopushDMLogin"), "api_key": c.s("octopushDMAPIKey"), "sms_recipients": c.s("octopushDMPhoneNumber"),
		"sms_sender": c.s("octopushDMSenderName"), "sms_type": kind, "transactional": "1", "sms_text": ascii(c.msg),
	} {
		values.Set(key, value)
	}
	res, err := c.post("https://www.octopush-dm.com/api/sms/json?"+values.Encode(), M{}, H{"cache-control": "no-cache"})
	if err != nil {
		return err
	}
	if code, ok := res.m()["error_code"]; ok && code != "000" {
		return fmt.Errorf("Octopush error %s", res.Text) //nolint:staticcheck // Provider name.
	}
	return nil
}

func oneBot(c *call) error {
	target := c.s("httpAddr")
	if !strings.HasPrefix(target, "http") {
		target = "http://" + target
	}
	if !strings.HasSuffix(target, "/") {
		target += "/"
	}
	data := M{"auto_escape": true, "message": "UptimeKuma Alert: " + c.msg}
	if c.s("msgType") == "group" {
		data["message_type"], data["group_id"] = "group", c.v("recieverId")
	} else {
		data["message_type"], data["user_id"] = "private", c.v("recieverId")
	}
	return c.postOnly(target+"send_msg", data, H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("accessToken")})
}

func oneChat(c *call) error {
	message := "Test Successful!"
	if c.hasBeat() {
		label := "[🟢 Up]"
		if c.status() == statusDown {
			label = "[🔴 Down]"
		} else if c.status() != statusUp {
			return nil
		}
		message = fmt.Sprintf("UptimeKuma Alert:\n%s\nName: %s\n%s\n%s", label, c.ms("name"), c.bs("msg"), c.timeLine())
	}
	return c.postOnly("https://chat-api.one.th/message/api/v1/push_message",
		M{"to": c.v("recieverId"), "bot_id": c.v("botId"), "type": "text", "message": message},
		H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("accessToken")})
}

func oneSender(c *call) error {
	beat, monitor := heartbeatAndMonitor(c)
	data := M{"heartbeat": beat, "monitor": monitor, "msg": c.msg, "type": "text", "recipient_type": "individual", "text": M{"body": c.msg}}
	if c.s("onesenderTypeReceiver") == "private" {
		data["to"] = c.s("onesenderReceiver") + "@s.whatsapp.net"
	} else {
		data["recipient_type"], data["to"] = "group", c.s("onesenderReceiver")+"@g.us"
	}
	return c.postOnly(c.s("onesenderURL"), data, H{"Authorization": "Bearer " + c.s("onesenderToken")})
}

var phoneSpace = regexp.MustCompile(`[\s+]`)
var sevenDigits = regexp.MustCompile(`^\d{7}$`)

func ooredoo(c *call) error {
	const perRequest = 20
	recipients := []string{}
	for _, raw := range regexp.MustCompile(`[\s,;]+`).Split(c.s("ooredooToNumber"), -1) {
		number := phoneSpace.ReplaceAllString(raw, "")
		if number == "" {
			continue
		}
		if sevenDigits.MatchString(number) {
			number = "960" + number
		}
		recipients = append(recipients, number)
	}
	if len(recipients) == 0 {
		return errors.New("no valid recipient phone number was provided")
	}
	target := js(c.or("ooredooServerUrl", "https://o-papi1-lb01.ooredoo.mv/bulk_sms/v2"))
	headers := H{"Content-Type": "application/x-www-form-urlencoded", "Authorization": "Bearer " + c.s("ooredooBearerToken")}
	for i := 0; i < len(recipients); i += perRequest {
		batch := recipients[i:min(i+perRequest, len(recipients))]
		res, err := c.post(target, encodeForm(form{
			{"username", c.s("ooredooUsername")}, {"access_key", encodeBase64(c.s("ooredooAccessKey"))},
			{"message", c.msg}, {"batch", strings.Join(batch, " ")},
		}), headers)
		if err != nil {
			return err
		}
		if code, ok := number(res.m()["response_code"]); !ok || code != 0 {
			return errors.New("Ooredoo rejected the message") //nolint:staticcheck // Provider name.
		}
	}
	return nil
}

func openWA(c *call) error {
	text, err := customText(c, "openwaUseCustomMessage", "openwaCustomMessage")
	if err != nil {
		return err
	}
	chats := splitTrim(c.s("openwaChatId"), ",")
	if len(chats) == 0 {
		return errors.New("no valid OpenWA chat ID found")
	}
	target := trimBase(c.s("openwaApiUrl")) + "/api/sessions/" + encodeURIComponent(c.s("openwaSession")) + "/messages/send-text"
	for _, chat := range chats {
		if err := c.postOnly(target, M{"chatId": chat, "text": text}, H{"Content-Type": "application/json", "X-Api-Key": c.s("openwaApiKey")}); err != nil {
			return err
		}
	}
	return nil
}

func opsgenie(c *call) error {
	target := "https://api.opsgenie.com/v2/alerts"
	if c.s("opsgenieRegion") == "eu" {
		target = "https://api.eu.opsgenie.com/v2/alerts"
	}
	headers := H{"Content-Type": "application/json", "Authorization": "GenieKey " + c.s("opsgenieApiKey")}
	switch {
	case !c.hasBeat():
		return c.postOnly(target, M{"message": c.msg, "alias": "uptime-kuma-notification-test", "source": "Uptime Kuma", "priority": "P5"}, headers)
	case c.status() == statusDown:
		return c.postOnly(target, M{
			"message": "Uptime Kuma Alert: " + c.ms("name"), "alias": c.mon["name"], "description": c.msg, "source": "Uptime Kuma",
			"priority": "P" + js(c.or("opsgeniePriority", 3)),
		}, headers)
	case c.status() == statusUp:
		return c.postOnly(target+"/"+encodeURIComponent(c.ms("name"))+"/close?identifierType=alias", M{"source": "Uptime Kuma"}, headers)
	}
	return nil
}

// sourceOf is the monitor's address as PagerDuty and Splunk show it.
func sourceOf(monitor M) string {
	if js(monitor["type"]) == "port" {
		source := js(monitor["hostname"])
		if truthy(monitor["port"]) {
			source += ":" + js(monitor["port"])
		}
		return source
	}
	if monitor["hostname"] != nil {
		return js(monitor["hostname"])
	}
	return js(monitor["url"])
}

func pagerDuty(c *call) error {
	title, body, action := "Uptime Kuma Alert", c.msg, "trigger"
	monitor := M{"type": "ping", "url": "Uptime Kuma Test Button"}
	switch {
	case c.isUp():
		title, body, monitor = "Uptime Kuma Monitor ✅ Up", c.bs("msg"), c.mon
		if c.s("pagerdutyAutoResolve") == "0" {
			return nil
		}
		action = c.s("pagerdutyAutoResolve")
	case c.isDown():
		title, body, monitor = "Uptime Kuma Monitor 🔴 Down", c.bs("msg"), c.mon
	case c.hasBeat():
		return nil
	}
	summary := "[" + title + "] " + body
	if truthy(monitor["name"]) {
		summary = "[" + title + "] [" + js(monitor["name"]) + "] " + body
	}
	dedup := "Uptime Kuma/test"
	if truthy(monitor["id"]) {
		dedup = "Uptime Kuma/" + js(monitor["id"])
	}
	return c.postOnly(c.s("pagerdutyIntegrationUrl"), M{
		"payload":     M{"summary": summary, "severity": c.or("pagerdutyPriority", "warning"), "source": sourceOf(monitor)},
		"routing_key": c.v("pagerdutyIntegrationKey"), "event_action": action, "dedup_key": dedup,
	}, H{"Content-Type": "application/json"})
}

func pagerTree(c *call) error {
	title, action := any(c.msg), "create"
	switch {
	case !c.hasBeat():
	case c.status() == statusUp && c.s("pagertreeAutoResolve") == "resolve":
		title, action = nil, "resolve"
	case c.status() == statusDown:
		title = `Uptime Kuma Monitor "` + c.ms("name") + `" is DOWN`
	default:
		return nil
	}
	id := any("uptime-kuma")
	if c.hasBeat() && truthy(c.beat["monitorID"]) {
		id = c.beat["monitorID"]
	}
	beat, monitor := heartbeatAndMonitor(c)
	return c.postOnly(c.s("pagertreeIntegrationUrl"), M{
		"event_type": action, "id": id, "title": title, "urgency": c.v("pagertreeUrgency"), "heartbeat": beat, "monitor": monitor,
	}, H{"Content-Type": "application/json"})
}

func pinglet(c *call) error {
	beat, monitor := heartbeatAndMonitor(c)
	return c.postOnly(strings.TrimSuffix(c.s("pingletPublishUrl"), "/")+"?rewrite=uptimekuma",
		M{"heartbeat": beat, "monitor": monitor, "msg": c.msg}, H{"Authorization": "Bearer " + c.s("pingletApiKey")})
}

func plivo(c *call) error {
	headers := H{"Content-Type": "application/json", "Authorization": basicAuth(c.s("plivoAuthID"), c.s("plivoAuthToken"))}
	base := "https://api.plivo.com/v1/Account/" + c.s("plivoAuthID")
	if c.s("plivoMessageType") == "call" {
		answer, err := url.Parse(c.s("plivoAnswerUrl"))
		if err != nil {
			return errors.New("invalid notification URL")
		}
		query := answer.Query()
		query.Set("message", c.msg)
		answer.RawQuery = query.Encode()
		return c.postOnly(base+"/Call/", M{"from": c.v("plivoFromNumber"), "to": c.v("plivoToNumber"), "answer_url": answer.String(), "answer_method": "GET"}, headers)
	}
	return c.postOnly(base+"/Message/", M{"src": c.v("plivoFromNumber"), "dst": c.v("plivoToNumber"), "text": c.msg}, headers)
}

func promoSMS(c *call) error {
	clean := ascii(c.msg)
	long := c.cfg["promosmsAllowLongSMS"]
	if long == nil {
		long = false
	}
	limit := 159
	if truthy(long) {
		limit = 639
	}
	res, err := c.post("https://promosms.com/api/rest/v3_2/sms", M{
		"recipients": []any{c.v("promosmsPhoneNumber")}, "text": jsSubstring(clean, limit), "long-sms": long,
		"type": jsNumber(c.s("promosmsSMSType")), "sender": c.v("promosmsSenderName"),
	}, H{"Content-Type": "application/json", "Authorization": basicAuth(c.s("promosmsLogin"), c.s("promosmsPassword"))})
	if err != nil {
		return err
	}
	if response, _ := res.m()["response"].(M); response != nil {
		if status, ok := number(response["status"]); !ok || status != 0 {
			return fmt.Errorf("something went wrong, the API returned %s", js(response["status"]))
		}
	}
	return nil
}

func pumble(c *call) error {
	attachment := M{"title": "Uptime Kuma Alert", "text": c.msg, "color": "#5BDD8B"}
	if c.hasBeat() {
		state, color := "down", "#DC3645"
		if c.status() == statusUp {
			state, color = "up", "#5BDD8B"
		}
		attachment = M{"title": c.ms("name") + " is " + state, "text": c.beat["msg"], "color": color}
	}
	return c.postOnly(c.s("webhookURL"), M{"attachments": []any{attachment}}, nil)
}

func pushbullet(c *call) error {
	data := M{"type": "note", "title": "Uptime Kuma Alert", "body": c.msg}
	if c.hasBeat() {
		label := "[✅ Up] "
		if c.status() == statusDown {
			label = "[🔴 Down] "
		} else if c.status() != statusUp {
			return nil
		}
		data = M{"type": "note", "title": "UptimeKuma Alert: " + c.ms("name"), "body": label + c.bs("msg") + "\n" + c.timeLine()}
	}
	return c.postOnly("https://api.pushbullet.com/v2/pushes", data, H{"Access-Token": c.s("pushbulletAccessToken"), "Content-Type": "application/json"})
}

func pushDeer(c *call) error {
	target := trimBase(strings.TrimSpace(js(c.or("pushdeerServer", "https://api2.pushdeer.com")))) + "/message/push"
	title := "## Uptime Kuma Message"
	if c.mon != nil && c.isUp() {
		title = "## Uptime Kuma: " + c.ms("name") + " up"
	} else if c.mon != nil && c.isDown() {
		title = "## Uptime Kuma: " + c.ms("name") + " down"
	}
	res, err := c.post(target, M{"pushkey": c.v("pushdeerKey"), "text": title, "desp": strings.ReplaceAll(c.msg, "\n", "\n\n"), "type": "markdown"}, nil)
	if err != nil {
		return err
	}
	data := res.m()
	if failure, ok := data["error"]; ok {
		return errors.New(js(failure))
	}
	content, _ := data["content"].(M)
	results, _ := content["result"].([]any)
	if len(results) == 0 {
		return errors.New("invalid PushDeer key")
	}
	var first M
	if text, ok := results[0].(string); !ok || json.Unmarshal([]byte(text), &first) != nil || first["success"] != "ok" {
		return errors.New("unknown error from PushDeer")
	}
	return nil
}

func pushover(c *call) error {
	data := M{
		"message": c.msg, "user": c.v("pushoveruserkey"), "token": c.v("pushoverapptoken"), "sound": c.v("pushoversounds"),
		"priority": c.v("pushoverpriority"), "title": c.v("pushovertitle"), "retry": "30", "expire": "3600", "html": 1,
	}
	if c.env.BaseURL != "" && c.mon != nil {
		data["url"], data["url_title"] = c.monitorURL(), "Link to Monitor"
	}
	if c.t("pushoverdevice") {
		data["device"] = c.cfg["pushoverdevice"]
	}
	if c.t("pushoverttl") {
		data["ttl"] = c.cfg["pushoverttl"]
	}
	if c.hasBeat() {
		if c.status() == statusUp && c.t("pushoversounds_up") {
			data["sound"] = c.cfg["pushoversounds_up"]
		}
		data["message"] = c.msg + "\n<b>Time (" + c.bs("timezone") + ")</b>: " + c.bs("localDateTime")
	}
	return c.postOnly("https://api.pushover.net/1/messages.json", data, nil)
}

func monitorTitle(c *call) string {
	if c.isUp() {
		return "UptimeKuma Monitor Up " + c.ms("name")
	}
	if c.isDown() {
		return "UptimeKuma Monitor Down " + c.ms("name")
	}
	return "UptimeKuma Message"
}

func pushPlus(c *call) error {
	return c.postOnly("https://www.pushplus.plus/send",
		M{"token": c.v("pushPlusSendKey"), "title": monitorTitle(c), "content": c.msg, "template": "html"}, H{"Content-Type": "application/json"})
}

func pushy(c *call) error {
	return c.postOnly("https://api.pushy.me/push?api_key="+c.s("pushyAPIKey"), M{
		"to": c.v("pushyToken"), "data": M{"message": "Uptime-Kuma"},
		"notification": M{"body": c.msg, "badge": 1, "sound": "ping.aiff"},
	}, nil)
}

func resend(c *call) error {
	fromName := strings.TrimSpace(c.s("resendFromName"))
	if fromName == "" {
		fromName = "Uptime Kuma"
	}
	return c.postOnly("https://api.resend.com/emails", M{
		"from": fromName + " <" + strings.TrimSpace(c.s("resendFromEmail")) + ">", "to": c.v("resendToEmail"),
		"subject": c.or("resendSubject", "Notification from Your Uptime Kuma"), "text": c.msg,
	}, H{"Authorization": "Bearer " + c.s("resendApiKey"), "Content-Type": "application/json"})
}

func rocketChat(c *call) error {
	target := c.s("rocketwebhookURL")
	base := M{"channel": c.v("rocketchannel"), "username": c.v("rocketusername"), "icon_emoji": c.v("rocketiconemo")}
	if !c.hasBeat() {
		base["text"] = c.msg
		return c.postOnly(target, base, nil)
	}
	attachment := M{
		"title": "Uptime Kuma Alert *Time (" + c.bs("timezone") + ")*\n" + c.bs("localDateTime"),
		"text":  "*Message*\n" + c.msg, "color": "#32cd32",
	}
	if c.status() == statusDown {
		attachment["color"] = "#ff0000"
	}
	if c.env.BaseURL != "" {
		attachment["title_link"] = c.monitorURL()
	}
	base["text"], base["attachments"] = "Uptime Kuma Alert", []any{attachment}
	return c.postOnly(target, base, nil)
}

func sendGrid(c *call) error {
	personalization := M{"to": []any{M{"email": c.v("sendgridToEmail")}}}
	if c.t("sendgridCcEmail") {
		personalization["cc"] = emailList(c.s("sendgridCcEmail"))
	}
	if c.t("sendgridBccEmail") {
		personalization["bcc"] = emailList(c.s("sendgridBccEmail"))
	}
	return c.postOnly("https://api.sendgrid.com/v3/mail/send", M{
		"personalizations": []any{personalization}, "from": M{"email": strings.TrimSpace(c.s("sendgridFromEmail"))},
		"subject": c.or("sendgridSubject", "Notification from Your Uptime Kuma"),
		"content": []any{M{"type": "text/plain", "value": c.msg}},
	}, H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("sendgridApiKey")})
}

var sctpKey = regexp.MustCompile(`(?i)^sctp(\d+)t`)

func serverChan(c *call) error {
	key := c.s("serverChanSendKey")
	target := "https://sctapi.ftqq.com/" + key + ".send"
	if m := sctpKey.FindStringSubmatch(key); m != nil {
		target = "https://" + m[1] + ".push.ft07.com/send/" + key + ".send"
	}
	return c.postOnly(target, M{"title": monitorTitle(c), "desp": c.msg}, nil)
}

func serwerSMS(c *call) error {
	data := M{"username": c.v("serwersmsUsername"), "password": c.v("serwersmsPassword"), "text": ascii(c.msg), "sender": c.v("serwersmsSenderName")}
	if c.s("serwersmsRecipientType") == "group" {
		data["group_id"] = c.v("serwersmsGroupId")
	} else {
		data["phone"] = c.v("serwersmsPhoneNumber")
	}
	res, err := c.post("https://api2.serwersms.pl/messages/send_sms", data, H{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	if !truthy(res.m()["success"]) {
		return errors.New("SerwerSMS.pl API returned an unexpected response") //nolint:staticcheck // Provider name.
	}
	return nil
}

func sevenIO(c *call) error {
	text := c.msg
	if c.hasBeat() {
		address := c.address()
		if address != "" {
			address = "(" + address + ") "
		}
		switch c.status() {
		case statusDown:
			text = fmt.Sprintf("Your service %s %swent down at %s (%s). Error: %s", c.ms("name"), address, c.bs("localDateTime"), c.bs("timezone"), c.bs("msg"))
		case statusUp:
			text = fmt.Sprintf("Your service %s %swent back up at %s (%s).", c.ms("name"), address, c.bs("localDateTime"), c.bs("timezone"))
		}
	}
	return c.postOnly("https://gateway.seven.io/api/sms", M{"to": c.v("sevenioReceiver"), "from": c.or("sevenioSender", "Uptime Kuma"), "text": text},
		H{"Content-Type": "application/json", "X-API-Key": c.s("sevenioApiKey")})
}

func signal(c *call) error {
	message := c.msg
	if c.t("signalUseTemplate") {
		rendered, err := c.render(c.s("signalTemplate"))
		if err != nil {
			return err
		}
		message = rendered
	}
	recipients := strings.Split(regexp.MustCompile(`\s`).ReplaceAllString(c.s("signalRecipients"), ""), ",")
	return c.postOnly(c.s("signalURL"), M{"message": message, "number": c.v("signalNumber"), "recipients": anyList(recipients)}, nil)
}

func signalgrid(c *call) error {
	kind, title := "INFO", "Uptime Kuma"
	if c.isDown() {
		kind, title = "CRIT", c.ms("name")+" Down"
	} else if c.isUp() {
		kind, title = "SUCCESS", c.ms("name")+" Up"
	}
	res, err := c.post("https://api.signalgrid.co/v1/push", form{
		{"client_key", c.s("signalgridClientKey")}, {"channel", c.s("signalgridChannel")}, {"title", title},
		{"body", c.msg}, {"type", kind}, {"critical", "false"},
	}, nil)
	if err != nil {
		return err
	}
	if code := res.m()["code"]; truthy(code) && js(code) != "200" {
		return errors.New(stringOr(res.m()["text"], "Signalgrid API request failed"))
	}
	return nil
}

func signl4(c *call) error {
	beat, monitor := heartbeatAndMonitor(c)
	data := M{"heartbeat": beat, "monitor": monitor, "msg": c.msg, "X-S4-SourceSystem": "UptimeKuma", "monitorUrl": c.address()}
	switch {
	case !c.hasBeat():
		data["title"], data["message"] = "Uptime Kuma Alert", c.msg
	case c.status() == statusUp:
		data["title"], data["X-S4-ExternalID"], data["X-S4-Status"] = "Uptime Kuma Monitor ✅ Up", "UptimeKuma-"+c.ms("id"), "resolved"
	case c.status() == statusDown:
		data["title"], data["X-S4-ExternalID"], data["X-S4-Status"] = "Uptime Kuma Monitor 🔴 Down", "UptimeKuma-"+c.ms("id"), "new"
	}
	return c.postOnly(c.s("webhookURL"), data, H{"Content-Type": "application/json"})
}

func slack(c *call) error {
	text := c.msg
	if c.t("slackchannelnotify") {
		text += " <!channel>"
	}
	target := c.s("slackwebhookURL")
	data := M{"text": text, "channel": c.v("slackchannel"), "username": c.v("slackusername"), "icon_emoji": c.v("slackiconemo")}
	if !c.hasBeat() {
		return c.postOnly(target, data, nil)
	}
	if c.t("slackUseTemplate") {
		rendered, err := renderWith(c.s("slackTemplate"), text, c.mon, c.beat, c.address())
		if err != nil {
			return err
		}
		data["text"] = rendered
		return c.postOnly(target, data, nil)
	}
	includeGroup := true
	if value, ok := c.cfg["slackIncludeGroupName"]; ok && value != nil {
		includeGroup = truthy(value)
	}
	groupPath := ""
	if path, ok := c.mon["path"].([]any); ok && len(path) > 1 {
		parts := make([]string, 0, len(path)-1)
		for _, part := range path[:len(path)-1] {
			parts = append(parts, js(part))
		}
		groupPath = strings.Join(parts, " / ")
	}
	title := stringOr(c.mon["name"], "Uptime Kuma Alert")
	data["attachments"] = []any{}
	if c.t("slackrichmessage") {
		blocks := []any{M{"type": "header", "text": M{"type": "plain_text", "text": title}}}
		if includeGroup && groupPath != "" {
			blocks = append(blocks, M{"type": "context", "elements": []any{M{"type": "mrkdwn", "text": "_" + groupPath + "_"}}})
		}
		blocks = append(blocks, M{"type": "section", "fields": []any{
			M{"type": "mrkdwn", "text": "*Message*\n" + text},
			M{"type": "mrkdwn", "text": "*Time (" + c.bs("timezone") + ")*\n" + c.bs("localDateTime")},
		}})
		actions := []any{}
		if c.env.BaseURL != "" {
			actions = append(actions, M{"type": "button", "text": M{"type": "plain_text", "text": "Visit Uptime Kuma"}, "value": "Uptime-Kuma", "url": c.monitorURL()})
		}
		if address := c.address(); address != "" {
			if parsed, err := url.ParseRequestURI(address); err == nil && parsed.Host != "" {
				actions = append(actions, M{"type": "button", "text": M{"type": "plain_text", "text": "Visit site"}, "value": "Site", "url": parsed.String()})
			}
		}
		if len(actions) > 0 {
			blocks = append(blocks, M{"type": "actions", "elements": actions})
		}
		color := "#e01e5a"
		if c.status() == statusUp {
			color = "#2eb886"
		}
		data["attachments"] = []any{M{"color": color, "blocks": blocks}}
	} else if includeGroup && groupPath != "" {
		data["text"] = "_" + groupPath + "_\n" + title + "\n" + text
	} else {
		data["text"] = title + "\n" + text
	}
	return c.postOnly(target, data, nil)
}

func smsGateway(c *call) error {
	target := strings.TrimRight(c.s("smsgatewayUrl"), "/") + "/api/v1/sms/send"
	failures := []string{}
	for _, to := range splitTrim(c.s("smsgatewayTo"), ",") {
		res, err := c.post(target, M{"to": to, "body": c.msg}, H{"X-API-Key": c.s("smsgatewayApiKey")})
		if err != nil {
			return err
		}
		if res.m()["status"] == "failed" {
			failures = append(failures, to+": "+stringOr(res.m()["message"], "unknown error"))
		}
	}
	if len(failures) > 0 {
		return errors.New("failed to send SMS to " + strings.Join(failures, "; "))
	}
	return nil
}

func smsPlanet(c *call) error {
	res, err := c.post("https://api2.smsplanet.pl/sms", multipartForm{
		{"from", c.s("smsplanetSenderName")}, {"to", c.s("smsplanetPhoneNumbers")}, {"msg", strings.Replace(c.msg, "🔴", "❌", 1)},
	}, H{"Authorization": "Bearer " + c.s("smsplanetApiToken"), "content-type": "multipart/form-data"})
	if err != nil {
		return err
	}
	if !truthy(res.m()["messageId"]) {
		return errors.New(stringOr(res.m()["errorMsg"], "SMSPlanet server did not respond with the expected result"))
	}
	return nil
}

func smsc(c *call) error {
	parts := []string{
		"fmt=3", "translit=" + c.s("smscTranslit"), "login=" + c.s("smscLogin"), "psw=" + c.s("smscPassword"),
		"phones=" + c.s("smscToNumber"), "mes=" + encodeURIComponent(ascii(c.msg)),
	}
	if sender, ok := c.cfg["smscSenderName"]; !ok || sender != "" {
		parts = append(parts, "sender="+c.s("smscSenderName"))
	}
	res, err := c.get("https://smsc.kz/sys/send.php?"+strings.Join(parts, "&"), H{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	if _, ok := res.m()["id"]; !ok {
		return fmt.Errorf("the API returned code %s: %s", js(res.m()["error_code"]), js(res.m()["error"]))
	}
	return nil
}

func defaulted(v any, fallback any) any {
	if v == nil {
		return fallback
	}
	return v
}

func numberList(s string) []any {
	out := []any{}
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			out = append(out, nil)
			continue
		}
		out = append(out, n)
	}
	return out
}

func smsEagle(c *call) error {
	msgType := c.s("smseagleMsgType")
	switch c.s("smseagleApiType") {
	case "smseagle-apiv1":
		method, recipientType, duration, voice := "", "", any(undef), any(nil)
		switch c.s("smseagleRecipientType") {
		case "smseagle-contact":
			recipientType, method = "contactname", "/send_tocontact"
		case "smseagle-group":
			recipientType, method = "groupname", "/send_togroup"
		case "smseagle-to":
			recipientType, method = "to", "/send_sms"
			if msgType != "smseagle-sms" {
				duration = defaulted(c.cfg["smseagleDuration"], 10)
				switch msgType {
				case "smseagle-ring":
					method = "/ring_call"
				case "smseagle-tts":
					method = "/tts_call"
				case "smseagle-tts-advanced":
					method = "/tts_adv_call"
					voice = c.or("smseagleTtsModel", 1)
				}
			}
		}
		u, err := url.Parse(c.s("smseagleUrl") + "/http_api" + method)
		if err != nil {
			return errors.New("invalid notification URL")
		}
		query := u.Query()
		query.Add("access_token", c.s("smseagleToken"))
		query.Add(recipientType, c.s("smseagleRecipient"))
		if kind := c.s("smseagleRecipientType"); kind == "" || kind == "smseagle-sms" {
			unicode := "0"
			if c.t("smseagleEncoding") {
				unicode = "1"
			}
			query.Add("unicode", unicode)
			query.Add("highpriority", js(defaulted(c.cfg["smseaglePriority"], "0")))
		} else {
			query.Add("duration", js(duration))
		}
		if c.s("smseagleRecipientType") != "smseagle-ring" {
			query.Add("message", c.msg)
		}
		if truthy(voice) {
			query.Add("voice_id", js(voice))
		}
		u.RawQuery = query.Encode()
		res, err := c.get(u.String(), H{"Content-Type": "application/x-www-form-urlencoded"})
		if err != nil {
			return err
		}
		if !strings.Contains(res.Text, "OK") {
			return errors.New("SMSEagle API returned an error: " + res.Text) //nolint:staticcheck // Provider name.
		}
		return nil
	case "smseagle-apiv2":
		encoding := "standard"
		if c.t("smseagleEncoding") {
			encoding = "unicode"
		}
		data := M{"text": c.msg, "encoding": encoding, "priority": defaulted(c.cfg["smseaglePriority"], 0)}
		if c.t("smseagleRecipientContact") {
			data["contacts"] = numberList(c.s("smseagleRecipientContact"))
		}
		if c.t("smseagleRecipientGroup") {
			data["groups"] = numberList(c.s("smseagleRecipientGroup"))
		}
		if c.t("smseagleRecipientTo") {
			data["to"] = anyList(strings.Split(c.s("smseagleRecipientTo"), ","))
		}
		endpoint := "/messages/sms"
		if msgType != "smseagle-sms" {
			data["duration"] = defaulted(c.cfg["smseagleDuration"], 10)
			switch msgType {
			case "smseagle-ring":
				endpoint = "/calls/ring"
			case "smseagle-tts":
				endpoint = "/calls/tts"
			case "smseagle-tts-advanced":
				endpoint = "/calls/tts_advanced"
				data["voice_id"] = defaulted(c.cfg["smseagleTtsModel"], 1)
			}
		}
		res, err := c.post(c.s("smseagleUrl")+"/api/v2"+endpoint, data, H{"access-token": c.s("smseagleToken"), "Content-Type": "application/json"})
		if err != nil {
			return err
		}
		list, _ := res.Data.([]any)
		for _, item := range list {
			if entry, _ := item.(M); entry["status"] == "queued" {
				return nil
			}
		}
		return errors.New("SMSEagle API returned an error: " + res.Text) //nolint:staticcheck // Provider name.
	}
	return nil
}

var whitespace = regexp.MustCompile(`\s`)

func smsIR(c *call) error {
	const maxLength = 20
	message := c.msg
	if len(utf16.Encode([]rune(message))) > maxLength {
		message = whitespace.ReplaceAllString(message, "")
	}
	if len(utf16.Encode([]rune(message))) > maxLength {
		message = jsSubstring(message, maxLength-1-3) + "..."
	}
	template, _ := strconv.Atoi(c.s("smsirTemplate"))
	templateValue := any(template)
	if _, err := strconv.Atoi(c.s("smsirTemplate")); err != nil {
		templateValue = nil // NaN in JSON.
	}
	for _, mobile := range strings.Split(c.s("smsirNumber"), ",") {
		if len(mobile) == 11 && strings.HasPrefix(mobile, "09") {
			if n, err := strconv.Atoi(mobile); err == nil && strconv.Itoa(n) == mobile[1:] {
				mobile = mobile[1:]
			}
		}
		err := c.postOnly("https://api.sms.ir/v1/send/verify", M{
			"mobile": mobile, "templateId": templateValue, "parameters": []any{M{"name": "uptkumaalert", "value": message}},
		}, H{"Content-Type": "application/json", "X-API-Key": c.s("smsirApiKey")})
		if err != nil {
			return err
		}
	}
	return nil
}

func smsManager(c *call) error {
	values := url.Values{}
	values.Set("apikey", c.s("smsmanagerApiKey"))
	values.Set("message", ascii(c.msg))
	values.Set("number", c.s("numbers"))
	values.Set("gateway", c.s("messageType"))
	_, err := c.get("https://http-api.smsmanager.cz/Send?"+values.Encode(), nil)
	return err
}

func smsPartner(c *call) error {
	res, err := c.post("https://api.smspartner.fr/v1/send", M{
		"apiKey": c.v("smspartnerApikey"), "sender": jsSubstring(c.s("smspartnerSenderName"), 11),
		"phoneNumbers": c.v("smspartnerPhoneNumber"), "message": jsSubstring(ascii(c.msg), 639),
	}, H{"Content-Type": "application/json", "cache-control": "no-cache"})
	if err != nil {
		return err
	}
	if res.m()["success"] != true {
		return errors.New("the SMSPartner API reported a failure")
	}
	return nil
}

func splunk(c *call) error {
	title, body, action := "Uptime Kuma Alert", c.msg, c.s("splunkSeverity")
	monitor := M{"type": "ping", "url": "Uptime Kuma Test Button"}
	switch {
	case c.isUp():
		title, body, monitor = "Uptime Kuma Monitor ✅ Up", c.bs("msg"), c.mon
		if c.s("splunkAutoResolve") == "0" {
			return nil
		}
		action = c.s("splunkAutoResolve")
	case c.isDown():
		title, body, monitor = "Uptime Kuma Monitor 🔴 Down", c.bs("msg"), c.mon
	case c.hasBeat():
		return nil
	}
	name, id := "undefined", "undefined"
	if monitor["name"] != nil {
		name = js(monitor["name"])
	}
	if monitor["id"] != nil {
		id = js(monitor["id"])
	}
	return c.postOnly(c.s("splunkRestURL"), M{
		"message_type": action, "state_message": "[" + title + "] [" + sourceOf(monitor) + "] " + body,
		"entity_display_name": "Uptime Kuma Alert: " + name, "routing_key": c.v("pagerdutyIntegrationKey"), "entity_id": "Uptime Kuma/" + id,
	}, H{"Content-Type": "application/json"})
}

func spugPush(c *call) error {
	title, content := "Uptime Kuma Message", c.msg
	if c.isUp() {
		title, content = "UptimeKuma 「"+c.ms("name")+"」 is Up", "[✅ Up] "+c.bs("msg")
	} else if c.isDown() {
		title, content = "UptimeKuma 「"+c.ms("name")+"」 is Down", "[🔴 Down] "+c.bs("msg")
	}
	return c.postOnly("https://push.spug.cc/send/"+c.s("templateKey"), M{"title": title, "content": content}, nil)
}

func squadcast(c *call) error {
	beat, _ := heartbeatAndMonitor(c)
	tags := M{}
	data := M{"message": c.msg, "description": "", "tags": tags, "heartbeat": beat, "source": "uptime-kuma"}
	if c.hasBeat() {
		data["description"], data["event_id"] = c.beat["msg"], c.beat["monitorID"]
		if c.status() == statusDown {
			data["message"], data["status"] = c.ms("name")+" is DOWN", "trigger"
		} else {
			data["message"], data["status"] = c.ms("name")+" is UP", "resolve"
		}
		tags["AlertAddress"] = c.address()
		list, _ := c.mon["tags"].([]any)
		for _, tag := range list {
			entry, _ := tag.(M)
			value := M{"value": entry["value"]}
			if entry["color"] != nil {
				value["color"] = entry["color"]
			}
			tags[js(entry["name"])] = value
		}
	}
	return c.postOnly(c.s("squadcastWebhookURL"), data, nil)
}

func stackfield(c *call) error {
	text := "+Uptime Kuma Alert+"
	if c.mon != nil && truthy(c.mon["name"]) {
		text += "\n*" + c.ms("name") + "*"
	}
	text += "\n" + c.msg
	if c.env.BaseURL != "" {
		if c.mon != nil {
			text += "\n" + c.monitorURL()
		} else {
			text += "\n" + c.env.BaseURL + "/"
		}
	}
	return c.postOnly(c.s("stackfieldwebhookURL"), M{"Title": text}, nil)
}

func teams(c *call) error {
	status := -1
	if c.hasBeat() {
		status = c.status()
	}
	name := ""
	if c.mon != nil {
		name = c.ms("name")
	}
	statusMessage := func(symbol bool) string {
		switch status {
		case statusDown:
			text := "[" + name + "] went down"
			if symbol {
				return "🔴 " + text
			}
			return text
		case statusUp:
			text := "[" + name + "] is back online"
			if symbol {
				return "✅ " + text
			}
			return text
		}
		return "Notification"
	}
	style := "emphasis"
	switch status {
	case statusDown:
		style = "attention"
	case statusUp:
		style = "good"
	}
	facts, actions := []any{}, []any{}
	if c.hasBeat() && c.env.BaseURL != "" {
		actions = append(actions, M{"type": "Action.OpenUrl", "title": "Visit Uptime Kuma", "url": c.monitorURL()})
	}
	description := c.msg
	if c.hasBeat() {
		description = c.bs("msg")
		if !truthy(c.beat["msg"]) {
			description = ""
		}
	}
	if description != "" {
		facts = append(facts, M{"title": "Description", "value": description})
	}
	if name != "" {
		facts = append(facts, M{"title": "Monitor", "value": name})
	}
	if address := c.address(); address != "" && address != "https://" {
		facts = append(facts, M{"title": "URL", "value": "[" + address + "](" + address + ")"})
		actions = append(actions, M{"type": "Action.OpenUrl", "title": "Visit Monitor URL", "url": address})
	}
	if c.hasBeat() && truthy(c.beat["localDateTime"]) {
		value := c.bs("localDateTime")
		if truthy(c.beat["timezone"]) {
			value += " (" + c.bs("timezone") + ")"
		}
		facts = append(facts, M{"title": "Time", "value": value})
	}
	body := []any{
		M{"type": "Container", "verticalContentAlignment": "Center", "items": []any{M{
			"type": "ColumnSet", "style": style,
			"columns": []any{
				M{"type": "Column", "width": "auto", "verticalContentAlignment": "Center", "items": []any{M{
					"type": "Image", "width": "32px", "style": "Person",
					"url": "https://raw.githubusercontent.com/louislam/uptime-kuma/master/public/icon.png", "altText": "Uptime Kuma Logo",
				}}},
				M{"type": "Column", "width": "stretch", "items": []any{
					M{"type": "TextBlock", "size": "Medium", "weight": "Bolder", "text": "**" + statusMessage(false) + "**"},
					M{"type": "TextBlock", "size": "Small", "weight": "Default", "text": "Uptime Kuma Alert", "isSubtle": true, "spacing": "None"},
				}},
			},
		}}},
		M{"type": "FactSet", "separator": false, "facts": facts},
	}
	if list, ok := c.mon["tags"].([]any); ok && c.hasBeat() && c.t("teamsEnableTags") && len(list) > 0 {
		items := []any{}
		for _, tag := range list {
			entry, _ := tag.(M)
			text := js(entry["name"])
			if entry["value"] != nil && entry["value"] != "" {
				text += ": " + js(entry["value"])
			}
			items = append(items, M{"type": "Badge", "text": text, "size": "Medium", "style": "Accent"})
		}
		body = append(body, M{
			"type":    "Container",
			"layouts": []any{M{"type": "Layout.Flow", "columnSpacing": "Small", "rowSpacing": "Small", "horizontalItemsAlignment": "Left"}},
			"items":   items,
		})
	}
	body = append(body, M{"type": "ActionSet", "actions": actions})
	return c.postOnly(c.s("webhookUrl"), M{"type": "message", "summary": statusMessage(true), "attachments": []any{M{
		"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": "",
		"content": M{"type": "AdaptiveCard", "body": body, "$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "version": "1.5"},
	}}}, nil)
}

func techulus(c *call) error {
	data := M{"title": c.or("pushTitle", "Uptime-Kuma"), "body": c.msg, "timeSensitive": defaulted(c.cfg["pushTimeSensitive"], true)}
	if c.t("pushChannel") {
		data["channel"] = c.cfg["pushChannel"]
	}
	if c.t("pushSound") {
		data["sound"] = c.cfg["pushSound"]
	}
	return c.postOnly("https://push.techulus.com/api/v1/notify/"+c.s("pushAPIKey"), data, nil)
}

var markdownV2Special = regexp.MustCompile(`[_*\[\]()~>#+\-=|{}.!\\]`)

func escapeMarkdownV2(text string) string {
	return markdownV2Special.ReplaceAllStringFunc(text, func(match string) string { return `\` + match })
}

func escapeDeep(value any) any {
	switch v := value.(type) {
	case string:
		return escapeMarkdownV2(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = escapeDeep(item)
		}
		return out
	case M:
		out := make(M, len(v))
		for key, item := range v {
			out[key] = escapeDeep(item)
		}
		return out
	}
	return value
}

func telegram(c *call) error {
	base := "https://api.telegram.org"
	if c.cfg["telegramServerUrl"] != nil {
		base = c.s("telegramServerUrl")
	}
	params := M{
		"chat_id": c.v("telegramChatID"), "text": c.msg,
		"disable_notification": defaulted(c.cfg["telegramSendSilently"], false),
		"protect_content":      defaulted(c.cfg["telegramProtectContent"], false),
		"link_preview_options": M{"is_disabled": true},
	}
	if c.t("telegramMessageThreadID") {
		params["message_thread_id"] = c.cfg["telegramMessageThreadID"]
	}
	if c.t("telegramUseTemplate") {
		mode := c.s("telegramTemplateParseMode")
		msg, monitor, beat := c.msg, c.mon, c.beat
		address := c.address()
		if mode == "MarkdownV2" {
			msg = escapeMarkdownV2(msg)
			if monitor != nil {
				monitor, _ = escapeDeep(monitor).(M)
				address = (&call{mon: monitor}).address()
			} else {
				monitor = M{
					"name":     escapeMarkdownV2("Monitor Name not available"),
					"hostname": escapeMarkdownV2("testing.hostname"),
					"url":      escapeMarkdownV2("testing.hostname"),
				}
				address = (&call{mon: monitor}).address()
			}
			if beat != nil {
				beat, _ = escapeDeep(beat).(M)
			}
		}
		rendered, err := renderWith(c.s("telegramTemplate"), msg, monitor, beat, address)
		if err != nil {
			return err
		}
		params["text"] = rendered
		if mode != "plain" {
			params["parse_mode"] = c.v("telegramTemplateParseMode")
		}
	}
	return c.postOnly(base+"/bot"+c.s("telegramBotToken")+"/sendMessage", params, nil)
}

func telnyx(c *call) error {
	data := M{"from": c.v("telnyxPhoneNumber"), "to": c.v("telnyxToNumber"), "text": c.msg}
	if c.t("telnyxMessagingProfileId") {
		data["messaging_profile_id"] = c.cfg["telnyxMessagingProfileId"]
	}
	return c.postOnly("https://api.telnyx.com/v2/messages", data, H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("telnyxApiKey")})
}

func teltonika(c *call) error {
	parsed, err := url.Parse(c.s("teltonikaUrl"))
	if err != nil || parsed.Host == "" {
		return errors.New("invalid Teltonika URL")
	}
	base := parsed.Scheme + "://" + parsed.Host
	headers := H{"Content-Type": "application/json", "cache-control": "no-cache"}
	login, err := c.post(base+"/api/login", M{"username": c.v("teltonikaUsername"), "password": c.v("teltonikaPassword")}, headers)
	if err != nil {
		return err
	}
	if login.m()["success"] != true {
		return errors.New("Teltonika login failed") //nolint:staticcheck // Provider name.
	}
	data, _ := login.m()["data"].(M)
	headers["Authorization"] = "Bearer " + js(data["token"])
	res, err := c.post(base+"/api/messages/actions/send", M{"data": M{
		"modem": c.v("teltonikaModem"), "number": c.v("teltonikaPhoneNumber"), "message": jsSubstring(c.msg, 159),
	}}, headers)
	if err != nil {
		return err
	}
	if res.m()["success"] != true {
		return errors.New("the Teltonika API reported a failure")
	}
	return nil
}

func threema(c *call) error {
	fields := form{{"from", c.s("threemaSenderIdentity")}, {"secret", c.s("threemaSecret")}, {"text", c.msg}}
	switch kind := c.s("threemaRecipientType"); kind {
	case "identity":
		fields = append(fields, [2]string{"to", c.s("threemaRecipient")})
	case "phone":
		fields = append(fields, [2]string{"phone", c.s("threemaRecipient")})
	case "email":
		fields = append(fields, [2]string{"email", c.s("threemaRecipient")})
	default:
		return fmt.Errorf("unsupported recipient type: %s", kind)
	}
	err := c.postOnly("https://msgapi.threema.ch/send_simple", fields, H{"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"})
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if message, ok := map[int]string{
			400: "invalid recipient identity or account not set up for basic mode (400)",
			401: "incorrect API identity or secret (401)", 402: "no credits remaining (402)",
			404: "recipient not found (404)", 413: "message is too long (413)", 500: "temporary internal server error (500)",
		}[httpErr.Status]; ok {
			return errors.New(message)
		}
	}
	return err
}

func commaTrim(s string) string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

func turboSMTP(c *call) error {
	host := "api.turbo-smtp.com"
	if c.s("turbosmtpRegion") == "eu" {
		host = "api.eu.turbo-smtp.com"
	}
	data := M{
		"from": strings.TrimSpace(c.s("turbosmtpFromEmail")), "to": c.v("turbosmtpToEmail"),
		"subject": c.or("turbosmtpSubject", "Notification from Your Uptime Kuma"), "content": c.msg,
	}
	if c.t("turbosmtpCcEmail") {
		data["cc"] = commaTrim(c.s("turbosmtpCcEmail"))
	}
	if c.t("turbosmtpBccEmail") {
		data["bcc"] = commaTrim(c.s("turbosmtpBccEmail"))
	}
	return c.postOnly("https://"+host+"/api/v2/mail/send", data,
		H{"Content-Type": "application/json", "consumerKey": c.s("turbosmtpConsumerKey"), "consumerSecret": c.s("turbosmtpConsumerSecret")})
}

func twilio(c *call) error {
	key := js(c.or("twilioApiKey", c.cfg["twilioAccountSID"]))
	fields := form{{"To", c.s("twilioToNumber")}, {"From", c.s("twilioFromNumber")}, {"Body", c.msg}}
	if c.t("twilioMessagingServiceSID") {
		fields = append(fields, [2]string{"MessagingServiceSid", c.s("twilioMessagingServiceSID")})
	}
	return c.postOnly("https://api.twilio.com/2010-04-01/Accounts/"+c.s("twilioAccountSID")+"/Messages.json", fields,
		H{"Content-Type": "application/x-www-form-urlencoded;charset=utf-8", "Authorization": basicAuth(key, c.s("twilioAuthToken"))})
}

func vk(c *call) error {
	noLinks := "0"
	if c.t("vkDontParseLinks") {
		noLinks = "1"
	}
	res, err := c.post("https://api.vk.ru/method/messages.send", form{
		{"access_token", c.s("vkAccessToken")}, {"v", c.s("vkApiVersion")}, {"peer_id", c.s("vkPeerId")}, {"message", c.msg},
		{"dont_parse_links", noLinks}, {"random_id", strconv.Itoa(int(math.Floor(c.env.RandFloat() * 2147483647)))},
	}, nil)
	if err != nil {
		return err
	}
	if failure, _ := res.m()["error"].(M); failure != nil {
		return fmt.Errorf("VK API returned error %s: %s", js(failure["error_code"]), js(failure["error_msg"])) //nolint:staticcheck // Provider name.
	}
	if _, ok := res.m()["response"]; !ok {
		return errors.New("invalid VK API response")
	}
	return nil
}

func vkTeams(c *call) error {
	base := strings.TrimSuffix(js(c.or("vkteamsBaseUrl", "https://myteam.mail.ru")), "/")
	fields := form{{"token", c.s("vkteamsBotToken")}, {"chatId", c.s("vkteamsChatId")}}
	text := c.msg
	if c.t("vkteamsUseTemplate") && c.t("vkteamsTemplate") {
		rendered, err := c.render(c.s("vkteamsTemplate"))
		if err != nil {
			return err
		}
		text = rendered
		if c.t("vkteamsTemplateFormat") && c.s("vkteamsTemplateFormat") != "plain" {
			fields = append(fields, [2]string{"parseMode", c.s("vkteamsTemplateFormat")})
		}
	}
	fields = append(fields, [2]string{"text", text})
	res, err := c.get(base+"/bot/v1/messages/sendText?"+encodeForm(fields), nil)
	if err != nil {
		return err
	}
	if res.m()["ok"] == false {
		return fmt.Errorf("VKTeams API returned an error: %s", js(res.m()["description"])) //nolint:staticcheck // Provider name.
	}
	return nil
}

func waha(c *call) error {
	return c.postOnly(trimBase(c.s("wahaApiUrl"))+"/api/sendText",
		M{"session": c.v("wahaSession"), "chatId": c.v("wahaChatId"), "text": c.msg},
		H{"Content-Type": "application/json", "X-Api-Key": c.s("wahaApiKey")})
}

func webhook(c *call) error {
	beat, monitor := heartbeatAndMonitor(c)
	method := strings.ToLower(c.s("httpMethod"))
	if method == "" {
		method = "post"
	}
	data := M{"heartbeat": beat, "monitor": monitor, "msg": c.msg}
	headers := H{}
	if raw := c.s("webhookAdditionalHeaders"); raw != "" {
		var extra map[string]any
		if json.Unmarshal([]byte(raw), &extra) != nil {
			return errors.New("additional headers aren't valid JSON")
		}
		for name, value := range extra {
			headers[name] = js(value)
		}
	}
	target := c.s("webhookURL")
	if method == "get" {
		parsed, err := url.Parse(target)
		if err != nil {
			return errors.New("invalid notification URL")
		}
		query := parsed.Query()
		query.Set("msg", c.msg)
		if c.hasBeat() {
			query.Set("heartbeat", js(c.beat))
		}
		if c.mon != nil {
			query.Set("monitor", js(c.mon))
		}
		parsed.RawQuery = query.Encode()
		_, err = c.get(parsed.String(), headers)
		return err
	}
	switch c.s("webhookContentType") {
	case "form-data":
		encoded, err := marshal(data)
		if err != nil {
			return err
		}
		return c.postOnly(target, multipartForm{{"data", string(encoded)}}, headers)
	case "custom":
		rendered, err := c.render(c.s("webhookCustomBody"))
		if err != nil {
			return err
		}
		return c.postOnly(target, rendered, headers)
	}
	return c.postOnly(target, data, headers)
}

func weCom(c *call) error {
	title := "UptimeKuma Message"
	if c.isUp() {
		title = "UptimeKuma Monitor Up"
	} else if c.isDown() {
		title = "UptimeKuma Monitor Down"
	}
	text := M{"content": title + "\n" + c.msg}
	if mentions := splitTrim(c.s("weComMentionedMobileList"), ","); len(mentions) > 0 {
		text["mentioned_mobile_list"] = anyList(mentions)
	}
	return c.postOnly("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key="+c.s("weComBotKey"), M{"msgtype": "text", "text": text}, H{"Content-Type": "application/json"})
}

func whapi(c *call) error {
	return c.postOnly(trimBase(js(c.or("whapiApiUrl", "https://gate.whapi.cloud/")))+"/messages/text",
		M{"to": c.v("whapiRecipient"), "body": c.msg}, H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("whapiAuthToken")})
}

func wPush(c *call) error {
	res, err := c.post("https://api.wpush.cn/api/v1/send", M{"title": monitorTitle(c), "content": c.msg, "apikey": c.v("wpushAPIkey"), "channel": c.v("wpushChannel")}, nil)
	if err != nil {
		return err
	}
	if code, ok := number(res.m()["code"]); !ok || code != 0 {
		return errors.New(js(res.m()["message"]))
	}
	return nil
}

func wxPusher(c *call) error {
	const perRequest = 10
	spts := splitTrim(c.s("wxpusherSPT"), ",")
	if len(spts) == 0 {
		return errors.New("no WxPusher SPT is configured")
	}
	summary := jsSubstring(monitorTitle(c), 100)
	for i := 0; i < len(spts); i += perRequest {
		res, err := c.post("https://wxpusher.zjiecode.com/api/send/message/simple-push", M{
			"content": c.msg, "summary": summary, "contentType": 1, "sptList": anyList(spts[i:min(i+perRequest, len(spts))]),
		}, nil)
		if err != nil {
			return err
		}
		if code, ok := number(res.m()["code"]); !ok || code != 1000 {
			return errors.New(js(res.m()["msg"]))
		}
	}
	return nil
}

func yzj(c *call) error {
	content := c.msg
	if c.hasBeat() {
		symbol := js(c.beat["status"])
		if c.status() == statusDown {
			symbol = "❌"
		} else if c.status() == statusUp {
			symbol = "✅"
		}
		content = symbol + " " + c.ms("name") + " \n> " + c.bs("msg") + "\n> " + c.timeLine()
	}
	res, err := c.post(c.s("yzjWebHookUrl")+"?yzjtype=0&yzjtoken="+c.s("yzjToken"), M{"content": content}, H{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	if !truthy(res.m()["success"]) {
		return errors.New(stringOr(res.m()["errmsg"], "yzj's server did not respond with the expected result"))
	}
	return nil
}

func zohoCliq(c *call) error {
	head, description, address := "Notification\n", c.msg, ""
	if c.hasBeat() {
		description, address = c.bs("msg"), c.address()
		if c.status() == statusDown {
			head = "🔴 [" + c.ms("name") + "] went down\n"
		} else if c.status() == statusUp {
			head = "### ✅ [" + c.ms("name") + "] is back online\n"
		}
	}
	lines := []string{head, "*Description:* " + description}
	if address != "" && address != "https://" {
		lines = append(lines, "*URL:* "+address)
	}
	return c.postOnly(c.s("webhookUrl"), M{"text": strings.Join(lines, "\n")}, nil)
}

// apprise runs the Apprise command line tool, which has to be installed separately.
func apprise(c *call) error {
	args := []string{"-vv", "-b", c.msg}
	if c.t("title") {
		args = append(args, "-t", c.s("title"))
	}
	args = append(args, "--", c.s("appriseURL"))
	output, err := exec.CommandContext(c.ctx, "apprise", args...).CombinedOutput() //nolint:gosec // Configured by an admin, as in the original.
	if err != nil {
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			return errors.New("the apprise command isn't installed")
		}
		return fmt.Errorf("apprise failed: %w", err)
	}
	if strings.Contains(string(output), "ERROR") {
		return errors.New("apprise reported an error")
	}
	return nil
}
