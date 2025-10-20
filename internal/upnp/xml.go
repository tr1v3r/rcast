package upnp

import (
	"encoding/xml"
	"html"
)

type DIDL struct {
	XMLName xml.Name `xml:"DIDL-Lite"`
	Items   []Item   `xml:"item"`
}

type Item struct {
	ID         string `xml:"id,attr"`
	ParentID   string `xml:"parentID,attr"`
	Restricted int    `xml:"restricted,attr"`
	Title      string `xml:"http://purl.org/dc/elements/1.1/ title"`
	Class      string `xml:"urn:schemas-upnp-org:metadata-1-0/upnp/ class"`
	Resources  []Res  `xml:"res"`
}

type Res struct {
	ProtocolInfo string `xml:"protocolInfo,attr"`
	URL          string `xml:",chardata"`
}

func ParseCurrentURIMetaData(metaEscaped string) (*DIDL, error) {
	// 1) 去除外层标签时的空白可选（如果需要）
	// 2) 反转义实体：&lt; -> <, &quot; -> "
	meta := html.UnescapeString(metaEscaped)

	var d DIDL
	if err := xml.Unmarshal([]byte(meta), &d); err != nil {
		return nil, err
	}
	return &d, nil
}
