package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/pion/webrtc/v4"
)

func signalHostSetup(signalServer string,
	hostId string,
	peerLocalSessionDescription webrtc.SessionDescription,
	peerIndex int) {

	client := &http.Client{}

	for {
		request, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("%s/api/host",
				signalServer),
			bytes.NewBufferString(
				fmt.Sprintf("{\"id\": \"%s\", \"description\": \"%s\"}",
					hostId,
					encode(
						&peerLocalSessionDescription,
					),
				),
			))
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: while setting up hostId with signalling server: %s\n",
				peerIndex,
				err)
			time.Sleep(1 * time.Second)
			continue
		}

		request.Header.Add("Content-type", "application/json; charset=UTF-8")

		hostSignal, err := client.Do(request)

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: while setting up hostId with signalling server: %s\n",
				peerIndex,
				err)
			time.Sleep(1 * time.Second)
			continue
		}
		allOK := hostSignal.StatusCode == http.StatusOK
		hostSignal.Body.Close()

		if allOK {
			// var hostSignalBody map[string]interface{}

			// json.NewDecoder(hostSignal.Body).Decode(hostSignalBody)
			break
		} else {
			fmt.Fprintf(os.Stderr,
				"conn %d: while setting up hostId with signalling server: %s\n",
				peerIndex,
				"response status")
			time.Sleep(1 * time.Second)
			continue
		}
	}
}

func signalWaitForHost(signalServer string,
	hostId string,
	peerIndex int) webrtc.SessionDescription {

	client := &http.Client{}

	for {
		params := url.Values{}
		params.Add("id", hostId)

		request, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf(
				"%s/api/host?%s",
				signalServer,
				params.Encode(),
			),
			nil)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: while getting host information with signalling server: %s\n",
				peerIndex,
				err)
			time.Sleep(1 * time.Second)
			continue
		}

		hostSignal, err := client.Do(request)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: while getting host information with signalling server: %s\n",
				peerIndex,
				err)
			time.Sleep(1 * time.Second)
			continue
		}
		var hostDescriptionObject map[string]string
		allOK := hostSignal.StatusCode == http.StatusOK
		if allOK {
			json.NewDecoder(hostSignal.Body).Decode(&hostDescriptionObject)
		}
		hostSignal.Body.Close()

		if allOK {

			hostDescription := hostDescriptionObject["description"]
			hostOffer := webrtc.SessionDescription{}
			decode(hostDescription, &hostOffer)

			return hostOffer
		} else {
			fmt.Fprintf(os.Stderr,
				"conn %d: while setting up hostId with signalling server: %s\n",
				peerIndex,
				"response status")
			time.Sleep(1 * time.Second)
			continue
		}
	}
}

func signalGuestSetup(signalServer string,
	hostId string,
	peerLocalSessionDescription webrtc.SessionDescription,
	peerIndex int) {

	client := &http.Client{}

	for {
		request, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("%s/api/guest",
				signalServer),
			bytes.NewBufferString(
				fmt.Sprintf("{\"hostId\": \"%s\", \"guestDescription\": \"%s\"}",
					hostId,
					encode(
						&peerLocalSessionDescription,
					),
				),
			))
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: while setting up guestDescription with signalling server: %s\n",
				peerIndex,
				err)
			time.Sleep(1 * time.Second)
			continue
		}

		request.Header.Add("Content-type", "application/json; charset=UTF-8")

		guestSignal, err := client.Do(request)

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: while setting up guestDescription with signalling server: %s\n",
				peerIndex,
				err)
			time.Sleep(1 * time.Second)
			continue
		}
		allOK := guestSignal.StatusCode == http.StatusOK
		guestSignal.Body.Close()

		if allOK {
			// var guestSignalBody map[string]interface{}

			// json.NewDecoder(hostSignal.Body).Decode(hostSignalBody)
			break
		} else {
			fmt.Fprintf(os.Stderr,
				"conn %d: while setting up guestDescription with signalling server: %s\n",
				peerIndex,
				"response status")
			time.Sleep(1 * time.Second)
			continue
		}
	}
}

func signalWaitForGuest(signalServer string,
	hostId string,
	peerIndex int,
	peerLocalSessionDescription webrtc.SessionDescription) webrtc.SessionDescription {

	client := &http.Client{}

	for {
		params := url.Values{}
		params.Add("hostId", hostId)

		request, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf(
				"%s/api/guest?%s",
				signalServer,
				params.Encode(),
			),
			nil)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: while getting guest information with signalling server: %s\n",
				peerIndex,
				err)
			time.Sleep(1 * time.Second)
			continue
		}

		guestSignal, err := client.Do(request)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: while getting guest information with signalling server: %s\n",
				peerIndex,
				err)
			time.Sleep(1 * time.Second)
			continue
		}

		var guestDescriptionObject map[string]string
		allOK := guestSignal.StatusCode == http.StatusOK
		if allOK {
			json.NewDecoder(guestSignal.Body).Decode(&guestDescriptionObject)
		}
		guestSignal.Body.Close()

		if allOK {
			guestDescription := guestDescriptionObject["guestDescription"]
			if guestDescription != "" {
				guestAnswer := webrtc.SessionDescription{}
				decode(guestDescription, &guestAnswer)

				return guestAnswer
			}

			fmt.Fprintf(os.Stderr,
				"conn %d: the guest has not signalled yet\n",
				peerIndex)
			time.Sleep(1 * time.Second)
		} else {
			fmt.Fprintf(os.Stderr,
				"conn %d: first need to create the host\n",
				peerIndex)

			signalHostSetup(signalServer,
				hostId,
				peerLocalSessionDescription,
				peerIndex)
		}
	}
}

// JSON encode + base64 a SessionDescription.
func encode(obj *webrtc.SessionDescription) string {
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}

	return base64.StdEncoding.EncodeToString(b)
}

// Decode a base64 and unmarshal JSON into a SessionDescription.
func decode(in string, obj *webrtc.SessionDescription) {
	b, err := base64.StdEncoding.DecodeString(in)
	if err != nil {
		panic(err)
	}

	if err = json.Unmarshal(b, obj); err != nil {
		panic(err)
	}
}
