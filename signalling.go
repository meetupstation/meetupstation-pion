package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/pion/webrtc/v4"
)

type SignallingScope struct {
	id        string
	accessKey string
	server    string

	complete                 bool
	localSessionDescription  string
	localCandidates          []string
	remoteSessionDescription string
	remoteCandidates         []string
	waitForAllICECandidates  <-chan struct{}
}

func (room *SignallingScope) init(server string, roomId string) {
	room.id = roomId
	room.accessKey = ""
	room.server = server
}

func (room *SignallingScope) restart() {
	room.complete = false
	room.localSessionDescription = ""
	room.localCandidates = []string{}
	room.remoteSessionDescription = ""
	room.remoteCandidates = []string{}
	room.waitForAllICECandidates = nil
}

func (room *SignallingScope) hostPost() error {

	client := &http.Client{}

	payload := struct {
		HostID      string   `json:"id"`
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
		AccessKey   string   `json:"accessKey"`
	}{
		HostID:      room.id,
		Description: room.localSessionDescription,
		Candidates:  room.localCandidates,
		AccessKey:   room.accessKey,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	request, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/host", room.server),
		bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	request.Header.Add("Content-type", "application/json; charset=UTF-8")

	hostSignal, err := client.Do(request)
	if err != nil {
		return err
	}
	defer hostSignal.Body.Close()

	if hostSignal.StatusCode != http.StatusOK {
		return fmt.Errorf("posting host signal: %s", "host is already in a call")
	}

	hostSignalBody := struct {
		ID        string `json:"id"`
		AccessKey string `json:"accessKey"`
	}{}
	if err := json.NewDecoder(hostSignal.Body).Decode(&hostSignalBody); err != nil {
		return err
	}

	room.id = hostSignalBody.ID
	room.accessKey = hostSignalBody.AccessKey

	return nil
}

func (room *SignallingScope) hostGet() (bool, error) {

	client := &http.Client{}

	params := url.Values{}
	params.Add("id", room.id)
	params.Add("accessKey", room.accessKey)

	request, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf(
			"%s/api/host?%s",
			room.server,
			params.Encode(),
		),
		nil)
	if err != nil {
		return false, err
	}

	hostSignal, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer hostSignal.Body.Close()

	if hostSignal.StatusCode != http.StatusOK {
		return true, fmt.Errorf("getting host signal: %s", "host not found or is already in a call")
	}

	hostSignalBody := struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
		AccessKey   string   `json:"accessKey"`
	}{}
	if err := json.NewDecoder(hostSignal.Body).Decode(&hostSignalBody); err != nil {
		return false, err
	}

	room.remoteSessionDescription = hostSignalBody.Description
	room.remoteCandidates = hostSignalBody.Candidates
	room.accessKey = hostSignalBody.AccessKey

	return false, nil
}

func (room *SignallingScope) guestPost() (bool, error) {

	client := &http.Client{}

	payload := struct {
		HostID      string   `json:"hostId"`
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
		AccessKey   string   `json:"accessKey"`
	}{
		HostID:      room.id,
		Description: room.localSessionDescription,
		Candidates:  room.localCandidates,
		AccessKey:   room.accessKey,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	request, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/guest",
			room.server),
		bytes.NewBuffer(body))
	if err != nil {
		return false, err
	}

	request.Header.Add("Content-type", "application/json; charset=UTF-8")

	guestSignal, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer guestSignal.Body.Close()

	if guestSignal.StatusCode != http.StatusOK {
		return true, fmt.Errorf("posting guest signal: %s", "host not found or is already in a call")
	}

	return true, nil
}

func (room *SignallingScope) guestGet() error {

	client := &http.Client{}

	params := url.Values{}
	params.Add("hostId", room.id)
	params.Add("accessKey", room.accessKey)

	request, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf(
			"%s/api/guest?%s",
			room.server,
			params.Encode(),
		),
		nil)
	if err != nil {
		return err
	}

	guestSignal, err := client.Do(request)
	if err != nil {
		return err
	}
	defer guestSignal.Body.Close()

	if guestSignal.StatusCode != http.StatusOK {
		return fmt.Errorf("getting guest signal: %s", "host not found or is already in a call")
	}

	guestSignalBody := struct {
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
	}{}
	if err := json.NewDecoder(guestSignal.Body).Decode(&guestSignalBody); err != nil {
		return fmt.Errorf("while setting up hostId with signalling server: %s", err)
	}

	room.remoteSessionDescription = guestSignalBody.Description
	room.remoteCandidates = guestSignalBody.Candidates

	return nil
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

func (room *SignallingScope) polling(peerConnectionId uint64,
	meetingType MeetingType,
	peerConnection *webrtc.PeerConnection) error {
	if room.complete {
		return nil
	}

	if meetingType == MeetingTypeHost {
		return room.hostPolling(peerConnection)
	} else {
		return room.guestPolling(peerConnection)
	}
}

func (room *SignallingScope) guestPolling(peerConnection *webrtc.PeerConnection) error {
	if room.complete {
		return nil
	}

	var err error
	room.complete, err = room.hostGet()
	if err != nil {
		room.accessKey = ""

		if room.complete {
			room.complete = false
			return nil
		}
		return err
	}
	room.complete = false

	if len(room.remoteSessionDescription) != 0 {
		var sessionDescription webrtc.SessionDescription
		decode(room.remoteSessionDescription, &sessionDescription)
		err = peerConnection.SetRemoteDescription(sessionDescription)
		if err != nil {
			return err
		}

		room.waitForAllICECandidates = webrtc.GatheringCompletePromise(peerConnection)

		answerSessionDescription, err := peerConnection.CreateAnswer(nil)
		if err != nil {
			return err
		}

		err = peerConnection.SetLocalDescription(answerSessionDescription)
		if err != nil {
			return err
		}
	} else if len(room.remoteCandidates) != 0 {
		// for (const candidate of room.remoteCandidates) {
		//     await peerConnection.addIceCandidate(
		//         JSON.parse(atob(candidate))
		//     );
		// }
	}

	room.remoteSessionDescription = ""
	room.remoteCandidates = []string{}

	select {
	case <-room.waitForAllICECandidates:
		room.waitForAllICECandidates = nil
		localDescription := peerConnection.LocalDescription()
		room.localSessionDescription = encode(localDescription)
	default:
	}

	if len(room.localSessionDescription) != 0 || len(room.localCandidates) != 0 {
		room.complete, err = room.guestPost()
		// room.signallingComplete becoming true regardless of ICE trickling proper
		// implementation might lead to no connection
		if err != nil {
			return err
		}
		room.localSessionDescription = ""
		room.localCandidates = []string{}
	}

	return nil
}

func (room *SignallingScope) hostPolling(peerConnection *webrtc.PeerConnection) error {
	if room.complete {
		return nil
	}

	var err error

	select {
	case <-room.waitForAllICECandidates:
		room.waitForAllICECandidates = nil
		localDescription := peerConnection.LocalDescription()
		room.localSessionDescription = encode(localDescription)
	default:
	}

	if len(room.localSessionDescription) != 0 || len(room.localCandidates) != 0 {
		err = room.hostPost()
		if err != nil {
			room.complete = true
			return err
		}
		room.localSessionDescription = ""
		room.localCandidates = []string{}
	}

	err = room.guestGet()
	if err != nil {
		room.complete = true
		return err
	}

	if len(room.remoteSessionDescription) != 0 {
		var sessionDescription webrtc.SessionDescription
		decode(room.remoteSessionDescription, &sessionDescription)
		peerConnection.SetRemoteDescription(sessionDescription)

		room.complete = true
	} else if len(room.remoteCandidates) != 0 {
		// for (const candidate of room.remoteCandidates) {
		//     await peerConnection.addIceCandidate(
		//         JSON.parse(atob(candidate))
		//     );
		// }
	}
	room.remoteSessionDescription = ""
	room.remoteCandidates = []string{}

	return nil
}
