package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

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

	localCandidatesMutex sync.Mutex
}

func (room *SignallingScope) OnICECandidate(candidate *webrtc.ICECandidate) {
	if candidate != nil {
		room.localCandidatesMutex.Lock()
		defer room.localCandidatesMutex.Unlock()

		decodedCandidate := candidate.ToJSON()
		encodedCandidate, err := encodeCandidate(&decodedCandidate)
		if err == nil {
			room.localCandidates = append(room.localCandidates, encodedCandidate)
		}
	}
}

func (room *SignallingScope) init(server string, roomId string) {
	room.id = roomId
	room.accessKey = ""
	room.server = server
}

func (room *SignallingScope) restart() {
	room.localCandidatesMutex.Lock()
	defer room.localCandidatesMutex.Unlock()

	room.complete = false
	room.localSessionDescription = ""
	room.localCandidates = []string{}
	room.remoteSessionDescription = ""
	room.remoteCandidates = []string{}
}

func (room *SignallingScope) hostPost(
	localSessionDescription string,
	localCandidates []string) error {

	payload := struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
		AccessKey   string   `json:"accessKey"`
	}{
		ID:          room.id,
		Description: localSessionDescription,
		Candidates:  localCandidates,
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

	client := http.Client{Timeout: 5 * time.Second}

	hostSignal, err := client.Do(request)
	if err != nil {
		return err
	}
	defer hostSignal.Body.Close()

	if hostSignal.StatusCode != http.StatusOK {
		return fmt.Errorf("posting host signal: %s",
			"room is already being negotiated at the moment")
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

	client := http.Client{Timeout: 5 * time.Second}

	hostSignal, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer hostSignal.Body.Close()

	if hostSignal.StatusCode != http.StatusOK {
		return true,
			fmt.Errorf("getting host signal: %s",
				"room is not found or already being negotiated at the moment")
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
	room.remoteCandidates = append(room.remoteCandidates, hostSignalBody.Candidates...)
	room.accessKey = hostSignalBody.AccessKey

	return false, nil
}

func (room *SignallingScope) guestPost(
	localSessionDescription string,
	localCandidates []string) (bool, error) {

	payload := struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
		AccessKey   string   `json:"accessKey"`
	}{
		ID:          room.id,
		Description: localSessionDescription,
		Candidates:  localCandidates,
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

	client := http.Client{Timeout: 5 * time.Second}

	guestSignal, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer guestSignal.Body.Close()

	if guestSignal.StatusCode != http.StatusOK {
		return true,
			fmt.Errorf("posting guest signal: %s",
				"room is not found or already being negotiated at the moment")
	}

	return false, nil
}

func (room *SignallingScope) guestGet() error {

	params := url.Values{}
	params.Add("id", room.id)
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

	client := http.Client{Timeout: 5 * time.Second}

	guestSignal, err := client.Do(request)
	if err != nil {
		return err
	}
	defer guestSignal.Body.Close()

	if guestSignal.StatusCode != http.StatusOK {
		return fmt.Errorf("getting guest signal: %s",
			"room is not found or already being negotiated at the moment")
	}

	guestSignalBody := struct {
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
	}{}
	if err := json.NewDecoder(guestSignal.Body).Decode(&guestSignalBody); err != nil {
		return fmt.Errorf("while setting up hostId with signalling server: %s", err)
	}

	room.remoteSessionDescription = guestSignalBody.Description
	room.remoteCandidates = append(room.remoteCandidates, guestSignalBody.Candidates...)

	return nil
}

func encodeSessionDescription(sessionDescription *webrtc.SessionDescription) (string, error) {
	if sessionDescription == nil {
		return "", nil
	}

	decodedSessionDescription, err := json.Marshal(sessionDescription)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(decodedSessionDescription), nil
}

func decodeSessionDescription(base64EncodedSessionDescription string) (webrtc.SessionDescription, error) {
	var sessionDescription webrtc.SessionDescription

	decodedSessionDescription, err := base64.StdEncoding.DecodeString(base64EncodedSessionDescription)
	if err != nil {
		return sessionDescription, err
	}

	err = json.Unmarshal(decodedSessionDescription, &sessionDescription)
	if err != nil {
		return sessionDescription, err
	}

	return sessionDescription, nil
}

func encodeCandidate(candidate *webrtc.ICECandidateInit) (string, error) {
	if candidate == nil {
		return "", nil
	}

	decodedCandidate, err := json.Marshal(candidate)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(decodedCandidate), nil
}

func decodeCandidate(base64EncodedCandidate string) (webrtc.ICECandidateInit, error) {
	var candidate webrtc.ICECandidateInit

	decodedCandidate, err := base64.StdEncoding.DecodeString(base64EncodedCandidate)
	if err != nil {
		return candidate, err
	}

	err = json.Unmarshal(decodedCandidate, &candidate)
	if err != nil {
		return candidate, err
	}

	return candidate, nil
}

func (room *SignallingScope) polling(meetingType MeetingType,
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
		if room.complete {
			if room.accessKey == "" {
				room.complete = false
			}
			return nil
		}
		return err
	}

	if room.remoteSessionDescription != "" {
		// prepareDataChannel(room, peerConnection);??
		sessionDescription, err := decodeSessionDescription(room.remoteSessionDescription)
		if err != nil {
			return err
		}
		room.remoteSessionDescription = ""

		if peerConnection.RemoteDescription() == nil {
			err = peerConnection.SetRemoteDescription(sessionDescription)
			if err != nil {
				return err
			}

			answerSessionDescription, err := peerConnection.CreateAnswer(nil)
			if err != nil {
				return err
			}

			err = peerConnection.SetLocalDescription(answerSessionDescription)
			if err != nil {
				return err
			}
		}
	}

	if len(room.remoteCandidates) != 0 {

		if peerConnection.RemoteDescription() != nil {
			encodedCandidates := room.remoteCandidates
			room.remoteCandidates = []string{}

			for _, encodedCandidate := range encodedCandidates {
				decodedCandidate, err := decodeCandidate(encodedCandidate)
				if err != nil {
					return err
				}

				err = peerConnection.AddICECandidate(decodedCandidate)
				if err != nil {
					return err
				}
			}
		}
	}

	localSessionDescription, err := encodeSessionDescription(peerConnection.LocalDescription())
	if err != nil {
		return err
	}

	room.localCandidatesMutex.Lock()
	localCandidates := room.localCandidates
	room.localCandidates = []string{}
	room.localCandidatesMutex.Unlock()

	if room.localSessionDescription != localSessionDescription ||
		len(localCandidates) != 0 {
		room.complete, err = room.guestPost(localSessionDescription, localCandidates)
		if err != nil {
			return err
		}
		room.localSessionDescription = localSessionDescription
	}

	return nil
}

func (room *SignallingScope) hostPolling(peerConnection *webrtc.PeerConnection) error {
	if room.complete {
		return nil
	}

	var err error

	localSessionDescription, err := encodeSessionDescription(peerConnection.LocalDescription())
	if err != nil {
		return err
	}

	room.localCandidatesMutex.Lock()
	localCandidates := room.localCandidates
	room.localCandidates = []string{}
	room.localCandidatesMutex.Unlock()

	if room.localSessionDescription != localSessionDescription ||
		len(localCandidates) != 0 {
		err = room.hostPost(localSessionDescription, localCandidates)
		if err != nil {
			return err
		}
		room.localSessionDescription = localSessionDescription
	}

	err = room.guestGet()
	if err != nil {
		return err
	}

	if len(room.remoteSessionDescription) != 0 {
		sessionDescription, err := decodeSessionDescription(room.remoteSessionDescription)
		if err != nil {
			return err
		}
		room.remoteSessionDescription = ""

		if peerConnection.RemoteDescription() == nil {

			err = peerConnection.SetRemoteDescription(sessionDescription)
			if err != nil {
				return err
			}
		}
	}

	if len(room.remoteCandidates) != 0 {

		if peerConnection.RemoteDescription() != nil {
			encodedCandidates := room.remoteCandidates
			room.remoteCandidates = []string{}
			for _, encodedCandidate := range encodedCandidates {
				decodedCandidate, err := decodeCandidate(encodedCandidate)
				if err != nil {
					return err
				}

				err = peerConnection.AddICECandidate(decodedCandidate)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}
