package httpbody

import (
	"bytes"
	"mime"
	"mime/multipart"
	"net/textproto"
)

// Part preserves field order, repeated names, and provider-specific file headers.
// Data and Text are alternatives; both remain read-only while the body is in use.
type Part struct {
	Header textproto.MIMEHeader
	Data   []byte
	Text   string
}

func Field(name, value string) Part {
	return Part{Header: textproto.MIMEHeader{"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": name})}}, Text: value}
}

func File(name, filename, contentType string, data []byte) Part {
	return Part{Header: textproto.MIMEHeader{
		"Content-Disposition": {mime.FormatMediaType("form-data", map[string]string{"name": name, "filename": filename})},
		"Content-Type":        {contentType},
	}, Data: data}
}

func Multipart(parts []Part) (*Body, error) {
	var framing bytes.Buffer
	writer := multipart.NewWriter(&framing)
	body := &Body{ContentType: writer.FormDataContentType()}
	for _, part := range parts {
		if _, err := writer.CreatePart(part.Header); err != nil {
			return nil, err
		}
		// Only MIME framing is copied; media never enters this buffer.
		body.addBytes(bytes.Clone(framing.Bytes()))
		framing.Reset()
		if part.Data != nil {
			body.addBytes(part.Data)
		} else {
			body.addString(part.Text)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	body.addBytes(bytes.Clone(framing.Bytes()))
	return body, nil
}
