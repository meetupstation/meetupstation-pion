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

func (room *Room) signalHostPost(signalServer string) error {

	client := &http.Client{}

	payload := struct {
		HostID      string   `json:"hostId"`
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
		AccessKey   string   `json:"accessKey"`
	}{
		HostID:      room.signalId,
		Description: room.localSessionDescription,
		Candidates:  room.localCandidates,
		AccessKey:   room.signalAccessKey,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	request, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/host", signalServer),
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
		return fmt.Errorf("posting host signal: %s\n", "response code")
	}

	hostSignalBody := struct {
		ID        string `json:"id"`
		AccessKey string `json:"accessKey"`
	}{}
	if err := json.NewDecoder(hostSignal.Body).Decode(&hostSignalBody); err != nil {
		return err
	}

	room.signalId = hostSignalBody.ID
	room.signalAccessKey = hostSignalBody.AccessKey

	return nil
}

func (room *Room) signalHostGet(signalServer string) (bool, error) {

	client := &http.Client{}

	params := url.Values{}
	params.Add("id", room.signalId)
	params.Add("accessKey", room.signalAccessKey)

	request, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf(
			"%s/api/host?%s",
			signalServer,
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
		return true, fmt.Errorf("getting host signal: %s\n", "response code / host does not exist")
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
	room.signalAccessKey = hostSignalBody.AccessKey

	return true, nil
}

func (room *Room) signalGuestPost(signalServer string) (bool, error) {

	client := &http.Client{}

	payload := struct {
		HostID      string   `json:"hostId"`
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
		AccessKey   string   `json:"accessKey"`
	}{
		HostID:      room.signalId,
		Description: room.localSessionDescription,
		Candidates:  room.localCandidates,
		AccessKey:   room.signalAccessKey,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	request, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/guest",
			signalServer),
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
		return true, fmt.Errorf("posting guest signal: %s\n", "response code / host does not exist")
	}

	return true, nil
}

func (room *Room) signalGuestGet(signalServer string) error {

	client := &http.Client{}

	params := url.Values{}
	params.Add("hostId", room.signalId)
	params.Add("accessKey", room.signalAccessKey)

	request, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf(
			"%s/api/guest?%s",
			signalServer,
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
		return fmt.Errorf("getting guest signal: %s\n", "response code / host does not exist")
	}

	guestSignalBody := struct {
		Description string   `json:"description"`
		Candidates  []string `json:"candidates"`
	}{}
	if err := json.NewDecoder(guestSignal.Body).Decode(&guestSignalBody); err != nil {
		return fmt.Errorf("while setting up hostId with signalling server: %s\n", err)
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
