package imageseg

import (
	"context"
	seg "github.com/alibabacloud-go/imageseg-20191230/v2/client"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
	pb "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeSegment struct {
	segmentAPI
	t   *testing.T
	url string
}

func (f fakeSegment) SegmentBodyAdvance(r *seg.SegmentBodyAdvanceRequest, _ *util.RuntimeOptions) (*seg.SegmentBodyResponse, error) {
	b, _ := io.ReadAll(r.ImageURLObject)
	if string(b) != "original-image" || tea.StringValue(r.ReturnForm) != "whiteBK" {
		f.t.Fatal("changed segmentation input")
	}
	return &seg.SegmentBodyResponse{Body: &seg.SegmentBodyResponseBody{Data: &seg.SegmentBodyResponseBodyData{ImageURL: tea.String(f.url)}}}, nil
}
func TestSegmentKeepsInputAndReturnForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("transparent-png")) }))
	defer srv.Close()
	p := &Provider{client: fakeSegment{t: t, url: srv.URL}, http: srv.Client()}
	req := &pb.GenerateRequest{Input: &pb.Input{Items: []*pb.InputItem{{Item: &pb.InputItem_Message{Message: &pb.Message{Role: pb.Role_ROLE_USER, Parts: []*pb.ContentPart{{Content: &pb.ContentPart_Text{Text: `{"return_form":"whiteBK"}`}}, {Content: &pb.ContentPart_Image{Image: &pb.Media{MimeType: "image/png", Source: &pb.Media_Data{Data: []byte("original-image")}}}}}}}}}}}
	out, err := p.GenerateImage(context.Background(), "aliyun-segment-body", req)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Items[0].GetImage().GetData()) != "transparent-png" {
		t.Fatal(out)
	}
}
