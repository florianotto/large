package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
)

type ffprobeResult struct {
	Streams []ffprobeStream
}

type ffprobeStream struct {
	Index  int
	Width  int
	Height int
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	videoMeta, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video with this ID does not exist", err)
		return
	}

	if videoMeta.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "This video belongs to another user", err)
		return
	}

	fmt.Printf("uploading video by user %s \n", userID)

	const maxMemory = 1 << 30 // 1 GB
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		fmt.Println("Error parsing multipart form data.")
	}

	file, contentHeader, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Invalid video", err)
		return
	}
	defer file.Close()

	fileContentType := contentHeader.Header.Get("Content-Type")
	if fileContentType == "" {
		respondWithError(w, http.StatusBadRequest, "No content-type", err)
		return
	}

	fileFormat, _, err := mime.ParseMediaType(fileContentType)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Media type invalid", err)
		return
	}

	if fileFormat != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Wrong video format", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create tmp file", err)
		return
	}
	//defer os.Remove(path.Join(os.TempDir(), "tubely-upload.mp4"))
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't write tmp file", err)
		return
	}

	// reset tmp file pointer to beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error with tmp file", err)
		return
	}

	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error with aspect ratio", err)
		return
	}
	var ratioPrefix string

	switch aspectRatio {
	case "16:9":
		ratioPrefix = "landscape/"
	case "9:16":
		ratioPrefix = "portrait/"
	case "other":
		ratioPrefix = "other/"
	}

	//set moov atom
	processedFile, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error setting moov atom.", err)
		return
	}
	defer os.Remove(processedFile)

	videoIDString = ratioPrefix + videoIDString

	fileToUpload, err := os.Open(processedFile)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed file.", err)
		return
	}
	defer fileToUpload.Close()

	// we only use mp4 here?
	key := fmt.Sprintf("%s.%s", videoIDString, "mp4")
	// put to s3
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &key,
		Body:        fileToUpload,
		ContentType: &fileContentType,
	})

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error putting object to S3", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, key)
	videoMeta.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(videoMeta)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video meta data", err)
		return
	}
	respondWithJSON(w, http.StatusOK, videoMeta)
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	out := bytes.Buffer{}
	stderr := bytes.Buffer{}
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		fmt.Print(stderr.String())
		fmt.Printf("Error parsing aspectRatio: %v", err)
		return "", err
	}

	res := ffprobeResult{}

	err = json.Unmarshal(out.Bytes(), &res)
	if err != nil {
		return "", err
	}

	if len(res.Streams) == 0 {
		return "", errors.New("Invalid format")
	}

	stream0 := res.Streams[0]
	width := stream0.Width
	height := stream0.Height

	if roundTo(float64(width)/float64(height), 1) == roundTo(16.0/9.0, 1) {
		return "16:9", nil
	}

	if roundTo(float64(width)/float64(height), 1) == roundTo(9.0/16.0, 1) {
		return "9:16", nil
	}

	return "other", nil
}

func roundTo(num float64, decimals int) float64 {
	output := math.Pow(10, float64(decimals))
	return math.Round(num*output) / output
}

func processVideoForFastStart(filePath string) (string, error) {

	processedFile := filePath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", processedFile)
	out := bytes.Buffer{}
	stderr := bytes.Buffer{}
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		fmt.Print(stderr.String())
		fmt.Printf("Error setting mov flags: %v", err)
		return "", err
	}
	return processedFile, nil
}
