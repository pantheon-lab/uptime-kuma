// Package badge draws shields.io style SVG badges. It's a port of badge-maker 3.3 (CC0), which
// the Node.js backend used, without logos and links, which Uptime Kuma never sets. Output is the
// same byte for byte, which the tests check against badge-maker's own output.
package badge

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// Badge is what to draw. Empty colours use badge-maker's defaults.
type Badge struct {
	Label      string
	Message    string
	Color      string
	LabelColor string
	// Style is flat, flat-square, plastic, for-the-badge or social. Empty means flat.
	Style string
}

// Styles are the styles Render accepts.
var Styles = []string{"plastic", "flat", "flat-square", "for-the-badge", "social"}

const (
	fontFamily        = `font-family="Verdana,Geneva,DejaVu Sans,sans-serif"`
	socialFontFamily  = `font-family="Helvetica Neue,Helvetica,Arial,sans-serif"`
	fontFamilyValue   = "Verdana,Geneva,DejaVu Sans,sans-serif"
	brightnessCutoff  = 0.69
	horizPadding      = 5
	defaultColor      = "#4c1"
	defaultLabelColor = "#555"
)

// Render returns the badge's SVG, or an error for an unknown style.
func Render(b Badge) (string, error) {
	label := jsTrim(b.Label)
	message := jsTrim(b.Message)
	color, colorSet := toSvgColor(b.Color)
	labelColor, labelColorSet := toSvgColor(b.LabelColor)
	var svg string
	switch b.Style {
	case "", "flat":
		svg = renderClassic(classicFlat, label, message, color, colorSet, labelColor, labelColorSet)
	case "flat-square":
		svg = renderClassic(classicFlatSquare, label, message, color, colorSet, labelColor, labelColorSet)
	case "plastic":
		svg = renderClassic(classicPlastic, label, message, color, colorSet, labelColor, labelColorSet)
	case "for-the-badge":
		svg = renderForTheBadge(label, message, color, colorSet, labelColor, labelColorSet)
	case "social":
		svg = renderSocial(label, message)
	default:
		return "", fmt.Errorf("style must be one of %s", strings.Join(Styles, ", "))
	}
	return stripXMLWhitespace(svg), nil
}

// jsSpace is JavaScript's \s, which badge-maker's whitespace handling uses.
const jsSpace = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

var (
	afterOpen   = regexp.MustCompile(`>[` + jsSpace + `]+`)
	beforeClose = regexp.MustCompile(`<[` + jsSpace + `]+`)
)

func jsTrim(s string) string { return strings.Trim(s, jsSpace) }

func stripXMLWhitespace(xml string) string {
	xml = afterOpen.ReplaceAllString(xml, ">")
	xml = beforeClose.ReplaceAllString(xml, "<")
	return jsTrim(xml)
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func escapeXML(s string) string { return xmlEscaper.Replace(s) }

// num prints a number as JavaScript does in a template string.
func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// jsLength is a string's length in UTF-16 code units, as JavaScript counts it.
func jsLength(s string) int { return len(utf16.Encode([]rune(s))) }

func roundUpToOdd(v int) int {
	if v%2 == 0 {
		return v + 1
	}
	return v
}

func preferredWidth(text string, f *font) int {
	return roundUpToOdd(int(f.widthOf(text)))
}

func textColors(color string) (text, shadow string) {
	if brightness(color) <= brightnessCutoff {
		return "#fff", "#010101"
	}
	return "#333", "#ccc"
}

func accessibleText(label, message string) string {
	if label != "" {
		return label + ": " + message
	}
	return message
}

// renderSVG wraps a badge. The width can be fractional, in for-the-badge.
func renderSVG(width float64, height int, accessible, main string) string {
	escaped := escapeXML(accessible)
	return fmt.Sprintf(`
    <svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="%s" height="%d" role="img" aria-label="%s">

    <title>%s</title>
    %s
    </svg>`, num(width), height, escaped, escaped, main)
}

type classicStyle struct {
	height         int
	verticalMargin int
	shadow         bool
	background     func(width, leftWidth, rightWidth, height int, labelColor, color string) string
}

var classicFlat = classicStyle{height: 20, shadow: true, background: func(width, leftWidth, rightWidth, height int, labelColor, color string) string {
	return fmt.Sprintf(`
      <linearGradient id="s" x2="0" y2="100%%">
        <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
        <stop offset="1" stop-opacity=".1"/>
      </linearGradient>

      <clipPath id="r">
        <rect width="%d" height="%d" rx="3" fill="#fff"/>
      </clipPath>

      <g clip-path="url(#r)">
        <rect width="%d" height="%d" fill="%s"/>
        <rect x="%d" width="%d" height="%d" fill="%s"/>
        <rect width="%d" height="%d" fill="url(#s)"/>
      </g>`, width, height, leftWidth, height, labelColor, leftWidth, rightWidth, height, color, width, height)
}}

var classicPlastic = classicStyle{height: 18, verticalMargin: -10, shadow: true, background: func(width, leftWidth, rightWidth, height int, labelColor, color string) string {
	return fmt.Sprintf(`
      <linearGradient id="s" x2="0" y2="100%%">
        <stop offset="0"  stop-color="#fff" stop-opacity=".7"/>
        <stop offset=".1" stop-color="#aaa" stop-opacity=".1"/>
        <stop offset=".9" stop-color="#000" stop-opacity=".3"/>
        <stop offset="1"  stop-color="#000" stop-opacity=".5"/>
      </linearGradient>

      <clipPath id="r">
        <rect width="%d" height="%d" rx="4" fill="#fff"/>
      </clipPath>

      <g clip-path="url(#r)">
        <rect width="%d" height="%d" fill="%s"/>
        <rect x="%d" width="%d" height="%d" fill="%s"/>
        <rect width="%d" height="%d" fill="url(#s)"/>
      </g>`, width, height, leftWidth, height, labelColor, leftWidth, rightWidth, height, color, width, height)
}}

var classicFlatSquare = classicStyle{height: 20, background: func(_, leftWidth, rightWidth, height int, labelColor, color string) string {
	return fmt.Sprintf(`
      <g shape-rendering="crispEdges">
        <rect width="%d" height="%d" fill="%s"/>
        <rect x="%d" width="%d" height="%d" fill="%s"/>
      </g>`, leftWidth, height, labelColor, leftWidth, rightWidth, height, color)
}}

// renderText is badge-maker's renderText, without links.
func (st classicStyle) renderText(leftMargin int, content, color string) (string, int) {
	if content == "" {
		return "", 0
	}
	textLength := preferredWidth(content, verdana11)
	escaped := escapeXML(content)
	x := 10 * (float64(leftMargin) + 0.5*float64(textLength) + horizPadding)
	textColor, shadowColor := textColors(color)
	out := ""
	if st.shadow {
		out = fmt.Sprintf(`<text aria-hidden="true" x="%s" y="%d" fill="%s" fill-opacity=".3" transform="scale(.1)" textLength="%d">%s</text>`,
			num(x), 150+st.verticalMargin, shadowColor, 10*textLength, escaped)
	}
	out += fmt.Sprintf(`<text x="%s" y="%d" transform="scale(.1)" fill="%s" textLength="%d">%s</text>`,
		num(x), 140+st.verticalMargin, textColor, 10*textLength, escaped)
	return out, textLength
}

func renderClassic(st classicStyle, label, message, color string, colorSet bool, labelColor string, labelColorSet bool) string {
	if !colorSet {
		color = defaultColor
	}
	hasLabel := label != "" || labelColorSet
	if !labelColorSet {
		labelColor = defaultLabelColor
	}
	if !hasLabel {
		labelColor = color
	}
	labelColor = escapeXML(labelColor)
	color = escapeXML(color)

	renderedLabel, labelWidth := st.renderText(1, label, labelColor)
	leftWidth := 0
	if hasLabel {
		leftWidth = labelWidth + 2*horizPadding
	}
	messageMargin := leftWidth
	if message != "" {
		messageMargin--
	}
	if !hasLabel {
		messageMargin++
	}
	renderedMessage, messageWidth := st.renderText(messageMargin, message, color)
	rightWidth := messageWidth + 2*horizPadding
	width := leftWidth + rightWidth

	main := st.background(width, leftWidth, rightWidth, st.height, labelColor, color) + fmt.Sprintf(`

      <g fill="#fff" text-anchor="middle" %s text-rendering="geometricPrecision" font-size="110">

        %s
        %s
      </g>`, fontFamily, renderedLabel, renderedMessage)
	return renderSVG(float64(width), st.height, accessibleText(label, message), main)
}

func renderForTheBadge(label, message, color string, colorSet bool, labelColor string, labelColorSet bool) string {
	const (
		fontSize      = 10
		badgeHeight   = 28
		textMargin    = 12
		letterSpacing = 1.25
	)
	label, message = strings.ToUpper(label), strings.ToUpper(message)
	if !colorSet {
		color = defaultColor
	}
	outLabelColor := labelColor
	if !labelColorSet {
		outLabelColor = defaultLabelColor
	}

	var labelTextWidth, messageTextWidth float64
	if label != "" {
		labelTextWidth = float64(int(verdana10.widthOf(label))) + letterSpacing*float64(jsLength(label))
	}
	if message != "" {
		messageTextWidth = float64(int(verdana10Bold.widthOf(message))) + letterSpacing*float64(jsLength(message))
	}
	hasLabel := label != ""
	labelTextMinX := float64(textMargin)
	var labelRectWidth, messageTextMinX, messageRectWidth float64
	if hasLabel {
		labelRectWidth = labelTextMinX + labelTextWidth + textMargin
		messageTextMinX = labelRectWidth + textMargin
	} else {
		messageTextMinX = textMargin
	}
	messageRectWidth = 2*textMargin + messageTextWidth

	var background string
	if hasLabel {
		background = fmt.Sprintf(`<rect width="%s" height="%d" fill="%s"/><rect x="%s" width="%s" height="%d" fill="%s"/>`,
			num(labelRectWidth), badgeHeight, escapeXML(outLabelColor), num(labelRectWidth), num(messageRectWidth), badgeHeight, escapeXML(color))
	} else {
		background = fmt.Sprintf(`<rect width="%s" height="%d" fill="%s"/>`, num(messageRectWidth), badgeHeight, escapeXML(color))
	}

	labelElement := ""
	if hasLabel {
		textColor, _ := textColors(outLabelColor)
		labelElement = fmt.Sprintf(`<text transform="scale(.1)" x="%s" y="175" textLength="%s" fill="%s">%s</text>`,
			num(10*(labelTextMinX+0.5*labelTextWidth)), num(10*labelTextWidth), escapeXML(textColor), escapeXML(label))
	}
	textColor, _ := textColors(color)
	messageElement := fmt.Sprintf(`<text transform="scale(.1)" x="%s" y="175" textLength="%s" fill="%s" font-weight="bold">%s</text>`,
		num(10*(messageTextMinX+0.5*messageTextWidth)), num(10*messageTextWidth), escapeXML(textColor), escapeXML(message))

	main := fmt.Sprintf(`<g shape-rendering="crispEdges">%s</g>`, background) +
		fmt.Sprintf(`<g fill="#fff" text-anchor="middle" font-family="%s" text-rendering="geometricPrecision" font-size="%d"> %s %s</g>`,
			escapeXML(fontFamilyValue), 10*fontSize, labelElement, messageElement)
	return renderSVG(labelRectWidth+messageRectWidth, badgeHeight, accessibleText(label, message), main)
}

func capitalize(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+len(string(r)):]
	}
	return s
}

func renderSocial(label, message string) string {
	const (
		internalHeight      = 19
		labelHorizPadding   = 5
		messageHorizPadding = 4
		horizGutter         = 6
	)
	label = capitalize(label)
	hasMessage := message != ""
	labelTextWidth := preferredWidth(label, helvetica11Bold)
	messageTextWidth := preferredWidth(message, helvetica11Bold)
	labelRectWidth := labelTextWidth + 2*labelHorizPadding
	messageRectWidth := messageTextWidth + 2*messageHorizPadding

	bubble := ""
	if hasMessage {
		mainX := float64(labelRectWidth+horizGutter) + 0.5
		bubble = fmt.Sprintf(`
      <rect x="%s" y="0.5" width="%d" height="%d" rx="2" fill="#fafafa"/>
      <rect x="%d" y="7.5" width="0.5" height="5" stroke="#fafafa"/>
      <path d="M%s 6.5 l-3 3v1 l3 3" stroke="d5d5d5" fill="#fafafa"/>
    `, num(mainX), messageRectWidth, internalHeight, labelRectWidth+horizGutter, num(mainX))
	}

	escapedLabel := escapeXML(label)
	labelX := 10 * (float64(labelTextWidth)/2 + labelHorizPadding)
	labelText := fmt.Sprintf(`
      <rect id="llink" stroke="#d5d5d5" fill="url(#a)" x=".5" y=".5" width="%d" height="%d" rx="2" />
      <text aria-hidden="true" x="%s" y="150" fill="#fff" transform="scale(.1)" textLength="%d">%s</text>
      <text x="%s" y="140" transform="scale(.1)" textLength="%d">%s</text>
    `, labelRectWidth, internalHeight, num(labelX), 10*labelTextWidth, escapedLabel, num(labelX), 10*labelTextWidth, escapedLabel)

	messageText := ""
	if hasMessage {
		escapedMessage := escapeXML(message)
		messageX := 10 * (float64(labelRectWidth+horizGutter) + float64(messageRectWidth)/2)
		messageText = fmt.Sprintf(`
      <text aria-hidden="true" x="%s" y="150" fill="#fff" transform="scale(.1)" textLength="%d">%s</text>
      <text id="rlink" x="%s" y="140" transform="scale(.1)" textLength="%d">%s</text>
    `, num(messageX), 10*messageTextWidth, escapedMessage, num(messageX), 10*messageTextWidth, escapedMessage)
	}

	rightWidth := 0
	if hasMessage {
		rightWidth = horizGutter + messageRectWidth
	}
	main := fmt.Sprintf(`
    <style>a:hover #llink{fill:url(#b);stroke:#ccc}a:hover #rlink{fill:#4183c4}</style>
    <linearGradient id="a" x2="0" y2="100%%">
      <stop offset="0" stop-color="#fcfcfc" stop-opacity="0"/>
      <stop offset="1" stop-opacity=".1"/>
    </linearGradient>
    <linearGradient id="b" x2="0" y2="100%%">
      <stop offset="0" stop-color="#ccc" stop-opacity=".1"/>
      <stop offset="1" stop-opacity=".1"/>
    </linearGradient>
    <g stroke="#d5d5d5">
      <rect stroke="none" fill="#fcfcfc" x="0.5" y="0.5" width="%d" height="%d" rx="2"/>
      %s
    </g>

    <g aria-hidden="true" fill="#333" text-anchor="middle" %s text-rendering="geometricPrecision" font-weight="700" font-size="110px" line-height="14px">
      %s
      %s
    </g>
    `, labelRectWidth, internalHeight, bubble, socialFontFamily, labelText, messageText)
	return renderSVG(float64(labelRectWidth+1+rightWidth), 20, accessibleText(label, message), main)
}
