package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// rest of lesson below
	const maxMemory = 10 << 20
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		fmt.Println("Error parsing multipart form data.")
	}

	file, contentHeader, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Invalid thumbnail", err)
		return
	}
	defer file.Close()

	fileContentType := contentHeader.Header.Get("Content-Type")
	if fileContentType == "" {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Invalid content-type for thumbnail.")
		return
	}

	fileExtension, err := parseExtension(fileContentType)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Invalid media type", err)
		return
	}

	// File saving to local system
	name := make([]byte, 32)
	_, err = rand.Read(name)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "For some reason creating a random name failed... %v", err)
	}
	encodedName := base64.RawURLEncoding.EncodeToString(name)
	fileName := fmt.Sprintf("%s.%s", encodedName, fileExtension)
	filePath := filepath.Join(cfg.assetsRoot, fileName)

	output, err := os.Create(filePath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error creating file: %v", err)
		return
	}

	_, err = io.Copy(output, file)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "Error writing file: %v", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "This video does not exist", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "This video belongs to another user", err)
		return
	}

	videoUrl := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, fileName)
	//encodedThumbnail := base64.StdEncoding.EncodeToString(fileData)
	//thumbnailData := fmt.Sprintf("data:%s;base64,%s", header.Header.Get("Content-Type"), encodedThumbnail)
	video.UpdatedAt = time.Now().UTC()
	video.ThumbnailURL = &videoUrl

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error updating video in db", err)
		return
	}
	respondWithJSON(w, http.StatusOK, database.Video{
		ID:           video.ID,
		CreatedAt:    video.CreatedAt,
		UpdatedAt:    video.UpdatedAt,
		ThumbnailURL: video.ThumbnailURL,
		VideoURL:     video.VideoURL,
	})

}

func parseExtension(contentType string) (string, error) {
	/*
			switch contentType {
			case "image/gif":
				return "gif", nil
			case "image/jpeg":
				return "jpg", nil
			case "image/png":
				return "png", nil
			case "image/tiff":
				return "tiff", nil
			default:
				return "", errors.New("Invalid content type.")
			}
		parts := strings.Split(contentType, "/")
		if len(parts) != 2 {
			return "bin"
		}
		return parts[1]
	*/
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", err
	}

	switch mediaType {
	case "image/jpeg":
		return "jpg", nil
	case "image/png":
		return "png", nil
	default:
		return "", errors.New("Invalid media type.")
	}
}
