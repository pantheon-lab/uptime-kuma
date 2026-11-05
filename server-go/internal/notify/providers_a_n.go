package notify

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Required by the Aliyun API.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func encodeBase64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

var providers = map[string]provider{}

func init() {
	for name, send := range map[string]provider{
		"Whatsapp360messenger": whatsapp360, "Elks": elks, "HaloPSA": haloPSA, "alerta": alerta, "AlertNow": alertNow,
		"AliyunSMS": aliyunSMS, "amootsms": amootSMS, "bale": bale, "Bark": bark, "bearsms": bearSMS, "Bitrix24": bitrix24,
		"Brevo": brevo, "CallMeBot": callMeBot, "Cellsynt": cellsynt, "clicksendsms": clickSend, "ClickUp": clickUp,
		"DingDing": dingDing, "discord": discord, "egosms": egoSMS, "evolution": evolution, "Feishu": feishu,
		"FlashDuty": flashDuty, "Flowtriq": flowtriq, "fluxer": fluxer, "FreeMobile": freeMobile, "GoAlert": goAlert,
		"GoogleChat": googleChat, "GoogleSheets": googleSheets, "gorush": gorush, "gotify": gotify, "GrafanaOncall": grafanaOncall,
		"gtxmessaging": gtxMessaging, "HeiiOnCall": heiiOnCall, "HomeAssistant": homeAssistant, "Indigo": indigo,
		"JiraServiceManagement": jiraServiceManagement, "Keep": keep, "Kook": kook, "line": line, "lunasea": lunaSea,
		"matrix": matrix, "mattermost": mattermost, "max": maxBot, "MicrosoftGraph": microsoftGraph, "Milky": milky,
		"nextcloudtalk": nextcloudTalk,
	} {
		providers[name] = send
	}
}

func heartbeatAndMonitor(c *call) (any, any) {
	var beat, monitor any
	if c.beat != nil {
		beat = c.beat
	}
	if c.mon != nil {
		monitor = c.mon
	}
	return beat, monitor
}

func stringOr(v any, fallback string) string {
	if truthy(v) {
		return js(v)
	}
	return fallback
}

func statusWord(c *call) string {
	if c.status() == statusDown {
		return "DOWN"
	}
	if c.status() == statusUp {
		return "UP"
	}
	return js(c.beat["status"])
}

var goTemplateName = regexp.MustCompile(`{{ monitorJSON\['name'\] }}`)
var goTemplateURL = regexp.MustCompile(`{{ monitorJSON\['url'\] }}`)
var goTemplateMsg = regexp.MustCompile(`{{ msg }}`)
var goTemplateIf = regexp.MustCompile(`(?s){% if monitorJSON %}(.*?){% endif %}`)

func whatsapp360(c *call) error {
	headers := H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("Whatsapp360messengerAuthToken")}
	message := c.msg
	if c.t("Whatsapp360messengerUseTemplate") && c.t("Whatsapp360messengerTemplate") {
		message = c.s("Whatsapp360messengerTemplate")
		if c.mon != nil {
			message = goTemplateName.ReplaceAllLiteralString(message, stringOr(c.mon["name"], ""))
			message = goTemplateURL.ReplaceAllLiteralString(message, stringOr(c.mon["url"], ""))
		}
		message = goTemplateMsg.ReplaceAllLiteralString(message, c.msg)
		message = goTemplateIf.ReplaceAllStringFunc(message, func(match string) string {
			if c.mon == nil {
				return ""
			}
			return goTemplateIf.FindStringSubmatch(match)[1]
		})
	}
	recipients := splitTrim(c.s("Whatsapp360messengerRecipient"), ";,")
	raw := c.cfg["Whatsapp360messengerGroupIds"]
	if !truthy(raw) {
		raw = c.cfg["Whatsapp360messengerGroupId"]
	}
	groups := []string{}
	switch value := raw.(type) {
	case []any:
		for _, item := range value {
			switch g := item.(type) {
			case string:
				if trimmed := strings.TrimSpace(g); trimmed != "" {
					groups = append(groups, trimmed)
				}
			case M:
				if truthy(g["id"]) {
					if trimmed := strings.TrimSpace(js(g["id"])); trimmed != "" {
						groups = append(groups, trimmed)
					}
				}
			}
		}
	case string:
		groups = splitTrim(value, ";,")
	}
	if len(recipients) == 0 && len(groups) == 0 {
		return errors.New("no recipient or group specified")
	}
	for _, recipient := range recipients {
		if err := c.postOnly("https://api.360messenger.com/v2/sendMessage", M{"phonenumber": recipient, "text": message}, headers); err != nil {
			return err
		}
	}
	for _, group := range groups {
		if err := c.postOnly("https://api.360messenger.com/v2/sendGroup", M{"groupId": group, "text": message}, headers); err != nil {
			return err
		}
	}
	return nil
}

func elks(c *call) error {
	return c.postOnly("https://api.46elks.com/a1/sms",
		form{{"from", c.s("elksFromNumber")}, {"to", c.s("elksToNumber")}, {"message", c.msg}},
		H{"Authorization": basicAuth(c.s("elksUsername"), c.s("elksAuthToken"))})
}

func haloPSA(c *call) error {
	status := "UNKNOWN"
	if c.isUp() {
		status = "UP"
	} else if c.isDown() {
		status = "DOWN"
	}
	monitorName, monitorID := any("No Monitor"), any(nil)
	if c.mon != nil {
		monitorName = stringOr(c.mon["name"], "No Monitor")
		monitorID = c.mon["id"]
	}
	headers := H{"Content-Type": "application/json"}
	if c.t("haloUsername") && c.t("haloPassword") {
		headers["Authorization"] = basicAuth(c.s("haloUsername"), c.s("haloPassword"))
	}
	return c.postOnly(c.s("halowebhookurl"), M{
		"title": "Uptime Kuma Alert", "status": status, "monitor": monitorName, "monitor_id": monitorID,
		"message": c.msg, "timestamp": isoNow(c), "uptime_kuma_version": "unknown",
	}, headers)
}

func alerta(c *call) error {
	headers := H{"Content-Type": "application/json;charset=UTF-8", "Authorization": "Key " + c.s("alertaApiKey")}
	data := M{
		"environment": c.v("alertaEnvironment"), "severity": "critical", "correlate": []any{}, "service": []any{"UptimeKuma"},
		"value": "Timeout", "tags": []any{"uptimekuma"}, "attributes": M{}, "origin": "uptimekuma", "type": "exceptionAlert",
	}
	if !c.hasBeat() {
		data["event"], data["text"], data["group"], data["resource"] = "msg", c.msg, "uptimekuma-msg", "Message"
		return c.postOnly(c.s("alertaApiEndpoint"), data, headers)
	}
	kind := c.ms("type")
	data["event"], data["group"], data["resource"] = c.mon["type"], "uptimekuma-"+kind, c.mon["name"]
	switch c.status() {
	case statusDown:
		data["severity"], data["text"] = c.v("alertaAlertState"), "Service "+kind+" is down."
	case statusUp:
		data["severity"], data["text"] = c.v("alertaRecoverState"), "Service "+kind+" is up."
	default:
		return nil
	}
	return c.postOnly(c.s("alertaApiEndpoint"), data, headers)
}

func alertNow(c *call) error {
	text, status, eventType := "", "open", "ERROR"
	eventID := strings.ReplaceAll(isoNow(c)[:10], "-", "")
	name := ""
	if c.mon != nil {
		name = c.ms("name")
	}
	if c.isUp() {
		text = "[" + name + "] ✅ Application is back online"
		status, eventType = "close", "INFO"
		eventID += "_" + regexp.MustCompile(`\s`).ReplaceAllString(name, "")
	} else if c.isDown() {
		text = "[" + name + "] 🔴 Application went down"
	}
	text += " - " + c.msg
	if c.env.BaseURL != "" && c.mon != nil {
		text += " >> " + c.monitorURL()
	}
	return c.postOnly(c.s("alertNowWebhookURL"), M{"summary": text, "status": status, "event_type": eventType, "event_id": eventID}, nil)
}

var (
	urlPattern    = regexp.MustCompile(`(?i)(?:https?|ftp|ws|wss)://\S+`)
	ipPattern     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b`)
	ipv6Pattern   = regexp.MustCompile(`\[?(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}\]?(?::\d+)?`)
	domainPattern = regexp.MustCompile(`\b(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}(?::\d+)?\b`)
	cidrPattern   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}/\d{1,2}\b`)
)

func removeIPAndDomain(message string) string {
	message = urlPattern.ReplaceAllString(message, "[URL]")
	message = ipPattern.ReplaceAllString(message, "[IP]")
	message = ipv6Pattern.ReplaceAllString(message, "[IP]")
	message = domainPattern.ReplaceAllString(message, "[Domain]")
	return cidrPattern.ReplaceAllString(message, "[CIDR]")
}

func aliyunSMS(c *call) error {
	name, at, status, optional := any(""), any(""), any(""), c.msg
	if c.hasBeat() {
		name, at, status, optional = c.mon["name"], c.beat["localDateTime"], statusWord(c), c.bs("msg")
	}
	// The field order is part of what gets signed, so this can't go through a map.
	parts := []string{}
	for _, field := range [][2]any{{"name", name}, {"time", at}, {"status", status}} {
		encoded, err := marshal(field[1])
		if err != nil {
			return err
		}
		parts = append(parts, `"`+field[0].(string)+`":`+string(encoded))
	}
	if c.t("optionalParameters") {
		encoded, err := marshal(removeIPAndDomain(optional))
		if err != nil {
			return err
		}
		parts = append(parts, `"msg":`+string(encoded))
	}
	encoded := []byte("{" + strings.Join(parts, ",") + "}")
	params := map[string]string{
		"PhoneNumbers": c.s("phonenumber"), "TemplateCode": c.s("templateCode"), "SignName": c.s("signName"),
		"TemplateParam": string(encoded), "AccessKeyId": c.s("accessKeyId"), "Format": "JSON",
		"SignatureMethod": "HMAC-SHA1", "SignatureVersion": "1.0",
		"SignatureNonce": strconv.FormatFloat(c.env.RandFloat(), 'f', -1, 64), "Timestamp": isoNow(c),
		"Action": "SendSms", "Version": "2017-05-25",
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts = make([]string, 0, len(keys))
	for _, key := range keys {
		value := encodeURIComponent(params[key])
		for from, to := range map[string]string{"!": "%21", "*": "%2A", "'": "%27", "(": "%28", ")": "%29"} {
			value = strings.ReplaceAll(value, from, to)
		}
		parts = append(parts, encodeURIComponent(key)+"="+value)
	}
	toSign := "POST&" + encodeURIComponent("/") + "&" + encodeURIComponent(strings.Join(parts, "&"))
	mac := hmac.New(sha1.New, []byte(c.s("secretAccessKey")+"&"))
	mac.Write([]byte(toSign))
	params["Signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	res, err := c.post("http://dysmsapi.aliyuncs.com/", values.Encode(), H{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return err
	}
	if message := js(res.m()["Message"]); message != "OK" {
		return errors.New(message)
	}
	return nil
}

func amootCheck(res *response) error {
	result := res.m()
	if result == nil {
		return errors.New("Amoot SMS API returned an unexpected response") //nolint:staticcheck // Provider name.
	}
	if status, ok := result["Status"].(string); ok && status != "" && status != "Success" {
		detail := ""
		if truthy(result["ErrorMessage"]) {
			detail = ": " + js(result["ErrorMessage"])
		}
		return fmt.Errorf("Amoot SMS API error: %s%s", status, detail) //nolint:staticcheck // Provider name.
	}
	return nil
}

func amootSMS(c *call) error {
	mobiles := splitTrim(c.s("amootMobiles"), ",")
	switch {
	case !c.t("amootApiToken"):
		return errors.New("Amoot SMS API token is required") //nolint:staticcheck // Provider name.
	case len(mobiles) == 0:
		return errors.New("at least one Amoot SMS recipient number is required")
	case c.t("amootUsePattern") && !c.t("amootPatternCodeId"):
		return errors.New("Amoot SMS Pattern Code ID is required") //nolint:staticcheck // Provider name.
	case (!c.t("amootUsePattern") || c.t("amootUseOwnLine")) && !c.t("amootLineNumber"):
		return errors.New("Amoot SMS line number is required") //nolint:staticcheck // Provider name.
	}
	token := c.s("amootApiToken")
	formHeaders := H{"Authorization": token, "Content-Type": "application/x-www-form-urlencoded"}
	if c.t("amootUsePattern") {
		for _, mobile := range mobiles {
			var res *response
			var err error
			if c.t("amootUseOwnLine") {
				res, err = c.post("https://portal.amootsms.com/rest/SendWithPatternOWN", M{
					"Token": token, "LineNumber": c.v("amootLineNumber"), "Mobile": mobile, "PatternCodeID": jsNumber(c.s("amootPatternCodeId")), "PatternValues": c.msg,
				}, H{"Authorization": "Bearer " + token, "Content-Type": "application/json"})
			} else {
				res, err = c.post("https://portal.amootsms.com/rest/SendWithPattern", form{
					{"Token", token}, {"Mobile", mobile}, {"PatternCodeID", c.s("amootPatternCodeId")}, {"PatternValues", c.msg},
				}, formHeaders)
			}
			if err != nil {
				return err
			}
			if err := amootCheck(res); err != nil {
				return err
			}
		}
		return nil
	}
	tehran, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		return err
	}
	res, err := c.post("https://portal.amootsms.com/rest/SendSimple", form{
		{"SendDateTime", c.env.Now().In(tehran).Format(time.DateTime)}, {"SMSMessageText", c.msg},
		{"LineNumber", c.s("amootLineNumber")}, {"Mobiles", strings.Join(mobiles, ",")},
	}, formHeaders)
	if err != nil {
		return err
	}
	if err := amootCheck(res); err != nil {
		return err
	}
	list, _ := res.m()["Data"].([]any)
	if len(list) == 0 {
		return errors.New("Amoot SMS API returned an unexpected response") //nolint:staticcheck // Provider name.
	}
	for _, item := range list {
		entry, _ := item.(M)
		if status := js(entry["Status"]); status != "1" && status != "Success" {
			return fmt.Errorf("Amoot SMS API error: %s", status) //nolint:staticcheck // Provider name.
		}
	}
	return nil
}

func bale(c *call) error {
	return c.postOnly("https://tapi.bale.ai/bot"+c.s("baleBotToken")+"/sendMessage",
		M{"chat_id": c.v("baleChatID"), "text": c.msg}, H{"content-type": "application/json"})
}

func bark(c *call) error {
	endpoint := strings.TrimSuffix(c.s("barkEndpoint"), "/")
	title := "UptimeKuma Message"
	if c.isUp() {
		title = "UptimeKuma Monitor Up"
	} else if c.isDown() {
		title = "UptimeKuma Monitor Down"
	}
	if version := c.cfg["apiVersion"]; version == "v1" || version == nil {
		group, sound := "UptimeKuma", "telegraph"
		if c.cfg["barkGroup"] != nil {
			group = c.s("barkGroup")
		}
		if c.cfg["barkSound"] != nil {
			sound = c.s("barkSound")
		}
		_, err := c.get(endpoint+"/"+encodeURIComponent(title)+"/"+encodeURIComponent(c.msg)+"?icon="+iconURL+"&group="+group+"&sound="+sound, nil)
		return err
	}
	return c.postOnly(endpoint, M{
		"title": title, "body": c.msg, "icon": iconURL,
		"sound": c.or("barkSound", "telegraph"), "group": c.or("barkGroup", "UptimeKuma"),
	}, nil)
}

var nonASCIIPresent = regexp.MustCompile(`[^\x00-\x7F]`)

func bearSMS(c *call) error {
	clean := strings.ReplaceAll(strings.ReplaceAll(c.msg, "🔴 ", ""), "✅ ", "")
	fields := form{{"app", "ws"}, {"u", c.s("bearsmsUsername")}, {"h", c.s("bearsmsHashKey")}, {"op", "pv"}, {"to", c.s("bearsmsPhoneNumber")}, {"msg", clean}}
	if c.t("bearsmsSenderId") {
		fields = append(fields, [2]string{"from", c.s("bearsmsSenderId")})
	}
	if nonASCIIPresent.MatchString(clean) {
		fields = append(fields, [2]string{"unicode", "1"})
	}
	res, err := c.get("https://app.bearsms.com/index.php?"+encodeForm(fields), nil)
	if err != nil {
		return err
	}
	data := res.m()
	results, _ := data["data"].([]any)
	ok := false
	for _, r := range results {
		if entry, _ := r.(M); entry["status"] == "OK" {
			ok = true
		}
	}
	if data["status"] == "ERR" || truthy(data["error_string"]) || !ok {
		if truthy(data["error_string"]) {
			return errors.New(js(data["error_string"]))
		}
		return errors.New(res.Text)
	}
	return nil
}

func bitrix24(c *call) error {
	color := "#67b518"
	if c.isUp() {
		color = "#b73419"
	}
	values := url.Values{}
	values.Set("user_id", c.s("bitrix24UserID"))
	values.Set("message", "[B]Uptime Kuma[/B]")
	values.Set("ATTACH[COLOR]", color)
	values.Set("ATTACH[BLOCKS][0][MESSAGE]", c.msg)
	_, err := c.get(c.s("bitrix24WebhookURL")+"/im.notify.system.add.json?"+values.Encode(), nil)
	return err
}

func emailList(s string) []any {
	out := []any{}
	for _, part := range strings.Split(s, ",") {
		out = append(out, M{"email": strings.TrimSpace(part)})
	}
	return out
}

func brevo(c *call) error {
	data := M{
		"sender":      M{"email": strings.TrimSpace(c.s("brevoFromEmail")), "name": c.or("brevoFromName", "Uptime Kuma")},
		"to":          []any{M{"email": c.v("brevoToEmail")}},
		"subject":     c.or("brevoSubject", "Notification from Your Uptime Kuma"),
		"htmlContent": "<html><head></head><body><p>" + strings.ReplaceAll(c.msg, "\n", "<br>") + "</p></body></html>",
	}
	if c.t("brevoCcEmail") {
		data["cc"] = emailList(c.s("brevoCcEmail"))
	}
	if c.t("brevoBccEmail") {
		data["bcc"] = emailList(c.s("brevoBccEmail"))
	}
	return c.postOnly("https://api.brevo.com/v3/smtp/email", data, H{"Content-Type": "application/json", "api-key": c.s("brevoApiKey")})
}

func callMeBot(c *call) error {
	u, err := url.Parse(c.s("callMeBotEndpoint"))
	if err != nil {
		return errors.New("invalid notification URL")
	}
	query := u.Query()
	query.Set("text", c.msg)
	u.RawQuery = query.Encode()
	_, err = c.get(u.String(), nil)
	return err
}

func cellsynt(c *call) error {
	concat := "1"
	if c.t("cellsyntAllowLongSMS") {
		concat = "6"
	}
	values := url.Values{}
	values.Set("username", c.s("cellsyntLogin"))
	values.Set("password", c.s("cellsyntPassword"))
	values.Set("destination", c.s("cellsyntDestination"))
	values.Set("text", ascii(c.msg))
	values.Set("originatortype", c.s("cellsyntOriginatortype"))
	values.Set("originator", c.s("cellsyntOriginator"))
	values.Set("allowconcat", concat)
	res, err := c.post("https://se-1.cellsynt.net/sms.php?"+values.Encode(), nil, H{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return err
	}
	if strings.Contains(res.Text, "Error:") {
		return errors.New(strings.ReplaceAll(res.Text, "Error:", ""))
	}
	return nil
}

func clickSend(c *call) error {
	_, err := c.post("https://rest.clicksend.com/v3/sms/send", M{"messages": []any{M{
		"body": ascii(c.msg), "to": c.v("clicksendsmsToNumber"), "source": "uptime-kuma", "from": c.v("clicksendsmsSenderName"),
	}}}, H{"Content-Type": "application/json", "Authorization": basicAuth(c.s("clicksendsmsLogin"), c.s("clicksendsmsPassword"))})
	return err
}

func clickUp(c *call) error {
	content := c.msg
	if c.hasBeat() {
		content += fmt.Sprintf("\n\n**Time (%s):** %s", c.bs("timezone"), c.bs("localDateTime"))
	}
	if address := c.address(); address != "" && !c.t("clickupDisableUrl") {
		content += "\n**Address:** " + address
	}
	return c.postOnly("https://api.clickup.com/api/v3/workspaces/"+c.s("clickupWorkspaceId")+"/chat/channels/"+c.s("clickupChannelId")+"/messages",
		M{"type": "message", "content": content, "content_format": "text/md"},
		H{"Authorization": c.s("clickupToken"), "Content-Type": "application/json"})
}

func anyOrEmpty(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return []any{}
}

func dingDing(c *call) error {
	mention := c.s("mentioning")
	mobiles, users := []any{}, []any{}
	if mention == "specify-mobiles" {
		mobiles = anyOrEmpty(c.cfg["mobileList"])
	}
	if mention == "specify-users" {
		users = anyOrEmpty(c.cfg["userList"])
	}
	// The original's operator precedence means that a mention list only adds a line break.
	mentionText := ""
	if len(mobiles)+len(users) > 0 {
		mentionText = "\n"
	}
	at := M{"isAtAll": mention == "everyone", "atUserIds": users, "atMobiles": mobiles}
	var params M
	if c.hasBeat() {
		title := "[" + statusWord(c) + "] " + c.ms("name")
		params = M{"msgtype": "markdown", "at": at, "markdown": M{
			"title": title,
			"text":  "## " + title + " \n> " + c.bs("msg") + "\n> " + c.timeLine() + mentionText,
		}}
	} else {
		params = M{"msgtype": "text", "at": at, "text": M{"content": c.msg + mentionText}}
	}
	timestamp := c.env.Now().UnixMilli()
	secret := c.s("secretKey")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d\n%s", timestamp, secret)
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	body, err := marshal(params)
	if err != nil {
		return err
	}
	res, err := c.post(fmt.Sprintf("%s&timestamp=%d&sign=%s", c.s("webHookUrl"), timestamp, encodeURIComponent(signature)),
		string(body), H{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	if message := js(res.m()["errmsg"]); message != "ok" {
		return errors.New(message)
	}
	return nil
}

func formatDuration(seconds int64) string {
	hours, minutes, rest := seconds/3600, (seconds%3600)/60, seconds%60
	parts := []string{}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	if rest > 0 && hours == 0 {
		parts = append(parts, fmt.Sprintf("%ds", rest))
	}
	if len(parts) == 0 {
		return "0s"
	}
	return strings.Join(parts, " ")
}

type chatWebhook struct {
	prefix        string
	withTimestamp bool
	extras        func(payload M)
}

// sendChatWebhook is Discord's format, which Fluxer shares.
func (w chatWebhook) send(c *call, target string) error {
	displayName := c.or(w.prefix+"Username", "Uptime Kuma")
	hasAvatar := true
	if info, err := c.get(target, nil); err == nil {
		hasAvatar = truthy(info.m()["avatar"])
	}
	format := c.s(w.prefix + "MessageFormat")
	if format == "" {
		format = "normal"
		if c.t(w.prefix + "UseMessageTemplate") {
			format = "custom"
		}
	}
	template := strings.TrimSpace(c.s(w.prefix + "MessageTemplate"))
	finish := func(payload M) error {
		if !hasAvatar {
			payload["avatar_url"] = iconURL
		}
		w.extras(payload)
		return c.postOnly(target, payload, nil)
	}
	if !c.hasBeat() {
		content := c.msg
		if format == "minimalist" {
			content = "Test: " + c.msg
		} else if format == "custom" && template != "" {
			rendered, err := c.render(template)
			if err != nil {
				return err
			}
			content = rendered
		}
		return finish(M{"username": displayName, "content": content})
	}
	if format == "minimalist" {
		content := "🟢 " + c.ms("name") + " is up."
		if c.status() == statusDown {
			content = "🔴 " + c.ms("name") + " is down."
		}
		return finish(M{"username": displayName, "content": content})
	}
	if format == "custom" && template != "" {
		rendered, err := c.render(template)
		if err != nil {
			return err
		}
		return finish(M{"username": displayName, "content": rendered})
	}

	unix := func(key string) int64 {
		at, _ := parseLocalUTC(c.bs(key))
		return at.Unix()
	}
	fields := []any{M{"name": "Service Name", "value": c.mon["name"]}}
	if address := c.address(); address != "" && !c.t("disableUrl") {
		label := "Service URL"
		if c.ms("type") == "push" {
			label = "Service Type"
		}
		fields = append(fields, M{"name": label, "value": address})
	}
	embed := M{}
	switch c.status() {
	case statusDown:
		fields = append(fields,
			M{"name": "Went Offline", "value": fmt.Sprintf("<t:%d:F>", unix("time"))},
			M{"name": "Time (" + c.bs("timezone") + ")", "value": c.beat["localDateTime"]},
			M{"name": "Error", "value": stringOr(c.beat["msg"], "N/A")})
		embed["title"], embed["color"] = "❌ Your service "+c.ms("name")+" went down. ❌", 16711680
	case statusUp:
		if truthy(c.beat["lastDownTime"]) {
			offline := unix("lastDownTime")
			fields = append(fields,
				M{"name": "Went Offline", "value": fmt.Sprintf("<t:%d:F>", offline)},
				M{"name": "Downtime Duration", "value": formatDuration(unix("time") - offline)})
		}
		fields = append(fields, M{"name": "Time (" + c.bs("timezone") + ")", "value": c.beat["localDateTime"]})
		if c.beat["ping"] != nil {
			fields = append(fields, M{"name": "Ping", "value": c.bs("ping") + " ms"})
		}
		embed["title"], embed["color"] = "✅ Your service "+c.ms("name")+" is up! ✅", 65280
	default:
		return nil
	}
	embed["fields"] = fields
	if w.withTimestamp {
		embed["timestamp"] = c.beat["time"]
	}
	payload := M{"username": displayName, "embeds": []any{embed}}
	if c.t(w.prefix + "PrefixMessage") {
		payload["content"] = c.cfg[w.prefix+"PrefixMessage"]
	}
	return finish(payload)
}

func discord(c *call) error {
	const suppressNotifications = 1 << 12
	u, err := url.Parse(c.s("discordWebhookUrl"))
	if err != nil {
		return errors.New("invalid notification URL")
	}
	if c.s("discordChannelType") == "postToThread" {
		query := u.Query()
		query.Add("thread_id", c.s("threadId"))
		u.RawQuery = query.Encode()
	}
	return chatWebhook{prefix: "discord", withTimestamp: true, extras: func(payload M) {
		if c.s("discordChannelType") == "createNewForumPost" {
			payload["thread_name"] = c.v("postName")
		}
		if c.t("discordSuppressNotifications") {
			payload["flags"] = suppressNotifications
		}
	}}.send(c, u.String())
}

func fluxer(c *call) error {
	return chatWebhook{prefix: "fluxer", extras: func(M) {}}.send(c, c.s("fluxerWebhookUrl"))
}

func egoSMS(c *call) error {
	_, err := c.get("https://www.egosms.co/api/v1/plain/?"+encodeForm(form{
		{"number", c.s("egosmsPhoneNumber")}, {"message", c.msg}, {"username", c.s("egosmsUsername")},
		{"password", c.s("egosmsPassword")}, {"sender", js(c.or("egosmsSender", "EGOSMS"))}, {"priority", "0"},
	}), nil)
	return err
}

var trailingSlashes = regexp.MustCompile(`([^/])/+$`)

func trimBase(s string) string { return trailingSlashes.ReplaceAllString(s, "$1") }

func customText(c *call, useField, templateField string) (string, error) {
	template := c.s(templateField)
	if c.t(useField) && strings.TrimSpace(template) != "" {
		return c.render(template)
	}
	return c.msg, nil
}

func evolution(c *call) error {
	text, err := customText(c, "evolutionUseCustomMessage", "evolutionCustomMessage")
	if err != nil {
		return err
	}
	base := trimBase(js(c.or("evolutionApiUrl", "https://evolapicloud.com/")))
	return c.postOnly(base+"/message/sendText/"+encodeURIComponent(c.s("evolutionInstanceName")),
		M{"number": c.v("evolutionRecipient"), "text": text},
		H{"Content-Type": "application/json", "apikey": c.s("evolutionAuthToken")})
}

func feishu(c *call) error {
	target := c.s("feishuWebHookUrl")
	if !c.hasBeat() {
		return c.postOnly(target, M{"msg_type": "text", "content": M{"text": c.msg}}, nil)
	}
	label, template := "[UP]", "green"
	if c.status() == statusDown {
		label, template = "[Down]", "red"
	} else if c.status() != statusUp {
		return nil
	}
	ping := "N/A"
	if c.beat["ping"] != nil {
		ping = c.bs("ping") + " ms"
	}
	content := "**Message**: " + c.bs("msg") + "\n**Ping**: " + ping + "\n**Time (" + c.bs("timezone") + ")**: " + c.bs("localDateTime")
	return c.postOnly(target, M{"msg_type": "interactive", "card": M{
		"config":   M{"update_multi": false, "wide_screen_mode": true},
		"header":   M{"title": M{"tag": "plain_text", "content": "UptimeKuma Alert: " + label + " " + c.ms("name")}, "template": template},
		"elements": []any{M{"tag": "div", "text": M{"tag": "lark_md", "content": content}}},
	}}, nil)
}

func flashDuty(c *call) error {
	title, body, status := "Uptime Kuma Alert", c.msg, "Info"
	monitor := M{"type": "ping", "url": c.msg, "name": "https://flashcat.cloud"}
	switch {
	case c.isUp():
		title, body, monitor, status = "Uptime Kuma Monitor ✅ Up", c.bs("msg"), c.mon, "Ok"
	case c.isDown():
		title, body, monitor = "Uptime Kuma Monitor 🔴 Down", c.bs("msg"), c.mon
		status = stringOr(c.cfg["flashdutySeverity"], "Info")
	}
	resource := ""
	switch {
	case js(monitor["type"]) == "port" && truthy(monitor["port"]):
		resource = js(monitor["hostname"]) + ":" + js(monitor["port"])
	case monitor["hostname"] != nil:
		resource = js(monitor["hostname"])
	default:
		resource = js(monitor["url"])
	}
	labels := M{"resource": resource, "check": monitor["name"]}
	if tags, ok := monitor["tags"].([]any); ok {
		for _, tag := range tags {
			if entry, ok := tag.(M); ok {
				labels[js(entry["name"])] = entry["value"]
			}
		}
	}
	key := strconv.FormatFloat(c.env.RandFloat(), 'f', -1, 64)
	if len(key) > 7 {
		key = key[7:]
	} else {
		key = ""
	}
	if truthy(monitor["id"]) {
		key = js(monitor["id"])
	}
	target := c.s("flashdutyIntegrationKey")
	if !strings.HasPrefix(target, "http") {
		target = "https://api.flashcat.cloud/event/push/alert/standard?integration_key=" + target
	}
	return c.postOnly(target, M{
		"description": "[" + title + "] [" + js(monitor["name"]) + "] " + body,
		"title":       title, "event_status": status, "alert_key": key, "labels": labels,
	}, H{"Content-Type": "application/json"})
}

func flowtriq(c *call) error {
	status, monitorName := "info", "Unknown"
	if c.mon != nil {
		monitorName = stringOr(c.mon["name"], "Unknown")
	}
	if c.isDown() {
		status = "down"
	} else if c.isUp() {
		status = "up"
	}
	data := M{"source": "uptime-kuma", "status": status, "monitor": monitorName, "msg": c.msg}
	if c.hasBeat() {
		data["heartbeat"] = M{"status": c.beat["status"], "time": c.beat["time"], "ping": c.beat["ping"], "msg": c.beat["msg"], "timezone": c.beat["timezone"]}
	}
	if c.mon != nil {
		data["monitorInfo"] = M{"id": c.mon["id"], "name": c.mon["name"], "type": c.mon["type"], "url": c.mon["url"], "hostname": c.mon["hostname"], "port": c.mon["port"]}
	}
	headers := H{"Content-Type": "application/json"}
	if c.t("flowtriqApiKey") {
		headers["X-API-Key"] = c.s("flowtriqApiKey")
	}
	return c.postOnly(c.s("flowtriqWebhookUrl"), data, headers)
}

func freeMobile(c *call) error {
	return c.postOnly("https://smsapi.free-mobile.fr/sendmsg?msg="+encodeURIComponent(strings.Replace(c.msg, "🔴", "⛔️", 1)),
		M{"user": c.v("freemobileUser"), "pass": c.v("freemobilePass")}, nil)
}

func goAlert(c *call) error {
	fields := multipartForm{{"summary", c.msg}}
	if c.isUp() {
		fields = append(fields, [2]string{"action", "close"})
	}
	return c.postOnly(c.s("goAlertBaseURL")+"/api/v2/generic/incoming?token="+c.s("goAlertToken"), fields, H{"Content-Type": "multipart/form-data"})
}

func googleChat(c *call) error {
	post := func(data M) error {
		retries := 1.0
		if n, ok := number(c.cfg["googleChatMaxRetries"]); ok && n > 0 {
			retries = math.Min(n, 10)
		}
		for {
			err := c.postOnly(c.s("googleChatWebhookURL"), data, nil)
			var httpErr *HTTPError
			if err == nil || !errors.As(err, &httpErr) || httpErr.Status != 429 {
				return err
			}
			retries--
			if retries <= 0 {
				return err
			}
			select {
			case <-c.ctx.Done():
				return err
			case <-time.After(time.Minute + time.Duration(c.env.RandFloat()*120)*time.Second):
			}
		}
	}
	if c.t("googleChatUseTemplate") && c.t("googleChatTemplate") {
		text, err := c.render(c.s("googleChatTemplate"))
		if err != nil {
			return err
		}
		return post(M{"text": text})
	}
	title := "Uptime Kuma Alert"
	if c.mon != nil && c.hasBeat() {
		if c.status() == statusUp {
			title = "✅ " + c.ms("name") + " is back online"
		} else {
			title = "🔴 " + c.ms("name") + " went down"
		}
	}
	paragraph := func(text string) M { return M{"textParagraph": M{"text": text}} }
	widgets := []any{paragraph("<b>Message:</b>\n" + c.msg)}
	if c.hasBeat() {
		widgets = append(widgets, paragraph("<b>Time ("+c.bs("timezone")+"):</b>\n"+c.bs("localDateTime")))
	}
	if address := c.address(); address != "" {
		widgets = append(widgets, paragraph("<b>Address:</b>\n"+address))
	}
	if c.env.BaseURL != "" {
		target := c.env.BaseURL + "/"
		if c.mon != nil {
			target = c.monitorURL()
		}
		widgets = append(widgets, M{"buttonList": M{"buttons": []any{M{"text": "Visit Uptime Kuma", "onClick": M{"openLink": M{"url": target}}}}}})
	}
	return post(M{"fallbackText": title, "cardsV2": []any{M{"card": M{"header": M{"title": title}, "sections": []any{M{"widgets": widgets}}}}}})
}

func googleSheets(c *call) error {
	status, monitorName, monitorURL, responseTime, statusCode := any("N/A"), any("N/A"), any("N/A"), any("N/A"), any("N/A")
	if c.mon != nil {
		monitorName = stringOr(c.mon["name"], "N/A")
		if address := c.address(); address != "" {
			monitorURL = address
		}
	}
	if c.hasBeat() {
		status = "UNKNOWN"
		if c.status() == statusDown {
			status = "DOWN"
		} else if c.status() == statusUp {
			status = "UP"
		}
		if truthy(c.beat["ping"]) {
			responseTime = c.beat["ping"]
		}
		if truthy(c.beat["statusCode"]) {
			statusCode = c.beat["statusCode"]
		}
	}
	return c.postOnly(c.s("googleSheetsWebhookUrl"), M{
		"timestamp": isoNow(c), "status": status, "monitorName": monitorName, "monitorUrl": monitorURL,
		"message": c.msg, "responseTime": responseTime, "statusCode": statusCode,
	}, H{"Content-Type": "application/json"})
}

func gorush(c *call) error {
	platform := map[string]any{"ios": 1, "android": 2, "huawei": 3}[c.s("gorushPlatform")]
	if platform == nil {
		platform = undef
	}
	retry, _ := strconv.Atoi(c.s("gorushRetry"))
	return c.postOnly(c.s("gorushServerURL")+"/api/push", M{"notifications": []any{M{
		"tokens": []any{c.v("gorushDeviceToken")}, "platform": platform, "message": c.msg, "title": c.v("gorushTitle"),
		"priority": c.v("gorushPriority"), "retry": retry, "topic": c.v("gorushTopic"),
	}}}, nil)
}

func gotify(c *call) error {
	return c.postOnly(strings.TrimSuffix(c.s("gotifyserverurl"), "/")+"/message?token="+c.s("gotifyapplicationToken"),
		M{"message": c.msg, "priority": c.or("gotifyPriority", 8), "title": "Uptime-Kuma"}, nil)
}

func grafanaOncall(c *call) error {
	if !c.t("GrafanaOncallURL") {
		return errors.New("GrafanaOncallURL cannot be empty")
	}
	data := M{"title": "General notification", "message": c.msg, "state": "alerting"}
	switch {
	case !c.hasBeat():
	case c.status() == statusDown:
		data = M{"title": c.ms("name") + " is down", "message": c.beat["msg"], "state": "alerting"}
	case c.status() == statusUp:
		data = M{"title": c.ms("name") + " is up", "message": c.beat["msg"], "state": "ok"}
	default:
		return nil
	}
	return c.postOnly(c.s("GrafanaOncallURL"), data, nil)
}

func gtxMessaging(c *call) error {
	text := strings.ReplaceAll(strings.ReplaceAll(c.msg, "🔴 ", ""), "✅ ", "")
	return c.postOnly("https://rest.gtx-messaging.net/smsc/sendsms/"+c.s("gtxMessagingApiKey")+"/json",
		form{{"from", strings.TrimSpace(c.s("gtxMessagingFrom"))}, {"to", strings.TrimSpace(c.s("gtxMessagingTo"))}, {"text", text}}, nil)
}

func heiiOnCall(c *call) error {
	payload := M{}
	for key, value := range c.beat {
		payload[key] = value
	}
	if c.env.BaseURL != "" && c.mon != nil {
		payload["url"] = c.monitorURL()
	}
	target := "https://heiioncall.com/triggers/" + c.s("heiiOnCallTriggerId") + "/"
	headers := H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("heiiOnCallApiKey")}
	switch {
	case !c.hasBeat():
		payload["msg"] = c.msg
		return c.postOnly(target+"alert", payload, headers)
	case c.status() == statusDown:
		return c.postOnly(target+"alert", payload, headers)
	case c.status() == statusUp:
		return c.postOnly(target+"resolve", payload, headers)
	}
	return nil
}

func homeAssistant(c *call) error {
	service := js(c.or("notificationService", "notify"))
	body := M{"title": "Uptime Kuma", "message": c.msg}
	if service != "persistent_notification" {
		name, status := any(undef), any(undef)
		if c.mon != nil {
			name = c.mon["name"]
		}
		if c.hasBeat() {
			status = c.beat["status"]
		}
		body["data"] = M{
			"name": name, "status": status, "channel": "Uptime Kuma",
			"icon_url": "https://github.com/louislam/uptime-kuma/blob/master/public/icon.png?raw=true",
		}
	}
	return c.postOnly(strings.TrimRight(strings.TrimSpace(c.s("homeAssistantUrl")), "/")+"/api/services/notify/"+service, body,
		H{"Authorization": "Bearer " + c.s("longLivedAccessToken"), "Content-Type": "application/json"})
}

func indigo(c *call) error {
	target := strings.TrimRight(strings.TrimSpace(c.s("indigoUrl")), "/") + "/v2/api/command"
	variable, _ := strconv.Atoi(c.s("indigoVariableId"))
	action, _ := strconv.Atoi(c.s("indigoActionGroupId"))
	if variable == 0 && action == 0 {
		return errors.New("set an Indigo variable ID, an action group ID, or both")
	}
	headers := H{"Authorization": "Bearer " + c.s("indigoApiKey"), "Content-Type": "application/json"}
	commands := []M{}
	if variable != 0 {
		commands = append(commands, M{"id": "uptime-kuma", "message": "indigo.variable.updateValue", "objectId": variable, "parameters": M{"value": c.msg}})
	}
	if action != 0 {
		commands = append(commands, M{"id": "uptime-kuma", "message": "indigo.actionGroup.execute", "objectId": action})
	}
	for _, command := range commands {
		res, err := c.post(target, command, headers)
		if err != nil {
			return err
		}
		if data := res.m(); truthy(data["error"]) || truthy(data["validationErrors"]) {
			return fmt.Errorf("Indigo rejected %s", js(command["message"])) //nolint:staticcheck // Provider name.
		}
	}
	return nil
}

func jiraServiceManagement(c *call) error {
	base := "https://api.atlassian.com/jsm/ops/api/" + c.s("jsmCloudId") + "/v1"
	headers := H{"Content-Type": "application/json", "Authorization": basicAuth(c.s("jsmEmail"), c.s("jsmApiToken"))}
	switch {
	case !c.hasBeat():
		return c.postOnly(base+"/alerts", M{
			"message": c.msg, "alias": "uptime-kuma-notification-test", "source": "Uptime Kuma", "priority": "P5", "tags": []any{"Uptime Kuma"},
		}, headers)
	case c.status() == statusDown:
		return c.postOnly(base+"/alerts", M{
			"message": "Uptime Kuma Alert: " + c.ms("name"), "alias": c.mon["name"], "description": c.msg, "source": "Uptime Kuma",
			"priority": "P" + js(c.or("jsmPriority", 3)), "tags": []any{"Uptime Kuma"},
		}, headers)
	case c.status() == statusUp:
		res, err := c.get(base+"/alerts/alias?alias="+encodeURIComponent(c.ms("name")), headers)
		if err != nil {
			return err
		}
		return c.postOnly(base+"/alerts/"+js(res.m()["id"])+"/close", M{"source": "Uptime Kuma"}, headers)
	}
	return nil
}

func keep(c *call) error {
	beat, monitor := heartbeatAndMonitor(c)
	return c.postOnly(strings.TrimSuffix(c.s("webhookURL"), "/")+"/alerts/event/uptimekuma",
		M{"heartbeat": beat, "monitor": monitor, "msg": c.msg},
		H{"x-api-key": c.s("webhookAPIKey"), "content-type": "application/json"})
}

func kook(c *call) error {
	return c.postOnly("https://www.kookapp.cn/api/v3/message/create", M{"target_id": c.v("kookGuildID"), "content": c.msg},
		H{"Authorization": "Bot " + c.s("kookBotToken"), "Content-Type": "application/json"})
}

func line(c *call) error {
	text := "Test Successful!"
	if c.hasBeat() {
		label := "[✅ Up]"
		if c.status() == statusDown {
			label = "[🔴 Down]"
		} else if c.status() != statusUp {
			return nil
		}
		text = "UptimeKuma Alert: " + label + "\nName: " + c.ms("name") + " \n" + c.bs("msg") + "\n" + c.timeLine()
	}
	return c.postOnly("https://api.line.me/v2/bot/message/push",
		M{"to": c.v("lineUserID"), "messages": []any{M{"type": "text", "text": text}}},
		H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("lineChannelAccessToken")})
}

func lunaSea(c *call) error {
	target := "device/" + c.s("lunaseaDevice")
	if c.s("lunaseaTarget") == "user" {
		target = "user/" + c.s("lunaseaUserID")
	}
	data := M{"title": "Uptime Kuma Alert", "body": c.msg}
	if c.hasBeat() {
		label := "[✅ Up] "
		if c.status() == statusDown {
			label = "[🔴 Down] "
		} else if c.status() != statusUp {
			return nil
		}
		data = M{"title": "UptimeKuma Alert: " + c.ms("name"), "body": label + c.bs("msg") + "\n" + c.timeLine()}
	}
	return c.postOnly("https://notify.lunasea.app/v1/custom/"+target, data, nil)
}

func matrix(c *call) error {
	random := base64.StdEncoding.EncodeToString(c.env.RandBytes(20))
	random = encodeURIComponent(random[:20])
	body := c.msg
	if c.t("matrixUseTemplate") {
		rendered, err := c.render(c.s("matrixTemplate"))
		if err != nil {
			return err
		}
		body = rendered
	}
	_, err := c.request("PUT", c.s("homeserverUrl")+"/_matrix/client/r0/rooms/"+encodeURIComponent(c.s("internalRoomId"))+"/send/m.room.message/"+random,
		M{"msgtype": "m.text", "body": body}, H{"Authorization": "Bearer " + c.s("accessToken")})
	return err
}

func mattermost(c *call) error {
	target := c.s("mattermostWebhookUrl")
	userName := js(c.or("mattermostusername", "Uptime Kuma"))
	if !c.hasBeat() {
		return c.postOnly(target, M{"username": userName, "text": c.msg}, nil)
	}
	channel := any(undef)
	if value, ok := c.cfg["mattermostchannel"].(string); ok {
		channel = strings.ToLower(value)
	}
	emoji := c.v("mattermosticonemo")
	online, offline := "", ""
	if text, ok := c.cfg["mattermosticonemo"].(string); ok && text != "" {
		if parts := strings.Split(text, " "); len(parts) >= 2 {
			online, offline = parts[0], parts[1]
		}
	}
	icon := emoji
	field := M{"short": false, "title": "Error", "value": c.beat["msg"]}
	statusText, color := "unknown", "#000000"
	switch c.status() {
	case statusDown:
		if offline != "" {
			icon = offline
		}
		statusText, color = "down.", "#FF0000"
	case statusUp:
		if online != "" {
			icon = online
		}
		field = M{"short": false, "title": "Ping", "value": c.bs("ping") + "ms"}
		statusText, color = "up!", "#32CD32"
	}
	return c.postOnly(target, M{
		"username": c.ms("name") + " " + userName, "channel": channel, "icon_emoji": icon, "icon_url": c.v("mattermosticonurl"),
		"attachments": []any{M{
			"fallback": "Your " + c.ms("pathName") + " service went " + statusText, "color": color,
			"title": c.ms("pathName") + " service went " + statusText, "title_link": c.mon["url"],
			"fields": []any{field, M{"short": true, "title": "Time (" + c.bs("timezone") + ")", "value": c.beat["localDateTime"]}},
		}},
	}, nil)
}

func maxBot(c *call) error {
	base := strings.TrimSuffix(js(c.or("maxApiUrl", "https://platform-api.max.ru")), "/")
	body := M{"text": c.msg}
	if c.t("maxUseTemplate") && c.t("maxTemplate") {
		rendered, err := c.render(c.s("maxTemplate"))
		if err != nil {
			return err
		}
		body["text"] = rendered
		if c.t("maxTemplateFormat") && c.s("maxTemplateFormat") != "plain" {
			body["format"] = c.cfg["maxTemplateFormat"]
		}
	}
	return c.postOnly(base+"/messages?chat_id="+encodeURIComponent(c.s("maxChatID")), body,
		H{"Authorization": c.s("maxBotToken"), "Content-Type": "application/json"})
}

func mailContent(c *call) (subject, body string, html bool, err error) {
	subject, body = c.msg, c.msg
	if c.hasBeat() {
		body = c.msg + "\n" + c.timeLine()
	}
	if (c.mon != nil && c.hasBeat()) || strings.HasSuffix(c.msg, "Testing") {
		if custom := strings.TrimSpace(c.s("customSubject")); custom != "" {
			if subject, err = c.render(custom); err != nil {
				return
			}
		}
		if custom := strings.TrimSpace(c.s("customBody")); custom != "" {
			html = c.t("htmlBody")
			if body, err = c.render(custom); err != nil {
				return
			}
		}
	}
	return
}

func microsoftGraph(c *call) error {
	recipients := func(field string) []any {
		out := []any{}
		for _, address := range splitTrim(c.s(field), ",;") {
			out = append(out, M{"emailAddress": M{"address": address}})
		}
		return out
	}
	to, cc, bcc := recipients("msGraphTo"), recipients("msGraphCC"), recipients("msGraphBCC")
	if len(to)+len(cc)+len(bcc) == 0 {
		return errors.New("at least one recipient (To, CC or BCC) is required")
	}
	mailbox := strings.TrimSpace(c.s("msGraphMailbox"))
	if mailbox == "" {
		return errors.New("sender mailbox is required")
	}
	subject, body, html, err := mailContent(c)
	if err != nil {
		return err
	}
	res, err := c.post("https://login.microsoftonline.com/"+encodeURIComponent(strings.TrimSpace(c.s("msGraphTenantId")))+"/oauth2/v2.0/token",
		encodeForm(form{
			{"client_id", strings.TrimSpace(c.s("msGraphClientId"))}, {"client_secret", c.s("msGraphClientSecret")},
			{"scope", "https://graph.microsoft.com/.default"}, {"grant_type", "client_credentials"},
		}), H{"Content-Type": "application/x-www-form-urlencoded"})
	if err != nil {
		return err
	}
	token := res.m()["access_token"]
	if !truthy(token) {
		return errors.New("failed to obtain access token from Microsoft Entra ID")
	}
	contentType := "Text"
	if html {
		contentType = "HTML"
	}
	return c.postOnly("https://graph.microsoft.com/v1.0/users/"+encodeURIComponent(mailbox)+"/sendMail", M{
		"message": M{
			"subject": subject, "body": M{"contentType": contentType, "content": body},
			"toRecipients": to, "ccRecipients": cc, "bccRecipients": bcc,
		},
		"saveToSentItems": false,
	}, H{"Authorization": "Bearer " + js(token), "Content-Type": "application/json"})
}

func milky(c *call) error {
	target := c.s("httpAddr")
	if !strings.HasPrefix(target, "http") {
		target = "http://" + target
	}
	if !strings.HasSuffix(target, "/") {
		target += "/api/"
	}
	data := M{"message": []any{M{"type": "text", "data": M{"text": "UptimeKuma Alert: " + c.msg}}}}
	if c.s("msgType") == "group" {
		target += "send_group_message"
		data["group_id"] = c.v("recieverId")
	} else {
		target += "send_private_message"
		data["user_id"] = c.v("recieverId")
	}
	return c.postOnly(target, data, H{"Content-Type": "application/json", "Authorization": "Bearer " + c.s("accessToken")})
}

func nextcloudTalk(c *call) error {
	random := hex.EncodeToString(c.env.RandBytes(64))[:64]
	mac := hmac.New(sha256.New, []byte(c.s("botSecret")))
	mac.Write([]byte(random + c.msg))
	// The original is `silentUp || silentDown`, so an unset option on a DOWN beat leaves the
	// field out, and on an UP beat sends false.
	body := M{"message": c.msg, "silent": false}
	if c.isUp() && truthy(c.cfg["sendSilentUp"]) {
		body["silent"] = c.cfg["sendSilentUp"]
	} else if c.isDown() {
		if value, ok := c.cfg["sendSilentDown"]; ok {
			body["silent"] = value
		} else {
			delete(body, "silent")
		}
	}
	return c.postOnly(c.s("host")+"/ocs/v2.php/apps/spreed/api/v1/bot/"+c.s("conversationToken")+"/message",
		body,
		H{"X-Nextcloud-Talk-Bot-Random": random, "X-Nextcloud-Talk-Bot-Signature": hex.EncodeToString(mac.Sum(nil)), "OCS-APIRequest": "true"})
}
