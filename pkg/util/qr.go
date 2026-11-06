package util

import (
	"fmt"
	"log"
	"os"

	"github.com/skip2/go-qrcode"
)

// QRCode renders url as a small QR code, if stderr is a terminal.
func QRCode(url string) (string, bool) {
	info, err := os.Stderr.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "", false
	}
	code, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		log.Printf("cannot render QR code: %v", err)
		return "", false
	}
	return code.ToSmallString(false), true
}

// showQRCode prints one address and then its code, as one group, for
// each address a command serves.
func ShowQRCode(url string) {
	if code, ok := QRCode(url); ok {
		fmt.Fprintf(os.Stderr, "\n%s\n%s\n", url, code)
	}
}
