package desktop

import "strings"

// Windows toast document (portable file on purpose: it is pure text, so it is unit
// tested on every platform, not only on a Windows host).
//
// The document is a ToastGeneric template with a protocol activation: clicking the
// toast (or its 打开 action) hands the URL to the shell's default handler, i.e. the
// browser. activationType="protocol" is what makes a click open the console page
// without gofer registering a COM notification activator of its own.
const toastXMLEnv = "GOFER_TOAST_XML"

func toastXML(n Notification) string {
	var b strings.Builder
	b.WriteString(`<toast`)
	if link := strings.TrimSpace(n.Link); link != "" {
		b.WriteString(` activationType="protocol" launch="`)
		b.WriteString(xmlAttr(link))
		b.WriteString(`"`)
	}
	b.WriteString(`><visual><binding template="ToastGeneric">`)
	b.WriteString(`<text>`)
	b.WriteString(xmlText(clampRunes(n.Title, 120)))
	b.WriteString(`</text>`)
	// One <text> element per body line: a newline inside a single element is rendered
	// inconsistently by the shell, separate elements always read as separate lines.
	written := 0
	for _, line := range strings.Split(n.Text, "\n") {
		if written >= 3 {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString(`<text>`)
		b.WriteString(xmlText(clampRunes(line, 240)))
		b.WriteString(`</text>`)
		written++
	}
	b.WriteString(`</binding></visual>`)
	if link := strings.TrimSpace(n.Link); link != "" {
		b.WriteString(`<actions><action content="打开" activationType="protocol" arguments="`)
		b.WriteString(xmlAttr(link))
		b.WriteString(`"/></actions>`)
	}
	b.WriteString(`</toast>`)
	return b.String()
}

// xmlText escapes a text node; xmlAttr an attribute value.
func xmlText(s string) string { return xmlEscape(s, false) }

func xmlAttr(s string) string { return xmlEscape(s, true) }

func xmlEscape(s string, attr bool) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			if attr {
				b.WriteString("&quot;")
				continue
			}
			b.WriteRune(r)
		case '\'':
			if attr {
				b.WriteString("&apos;")
				continue
			}
			b.WriteRune(r)
		case '\n', '\r', '\t':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
