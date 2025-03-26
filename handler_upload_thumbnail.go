package main

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
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

	const maxMemory = 10 << 20
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Too large a request", err)
		return
	}
	data, head, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Ill-formed request", err)
	}
	cType, _, _ := mime.ParseMediaType(head.Header.Get("Content-Type"))
	supportedTypes := []string{"image/png", "image/jpeg"}
	if !slices.Contains(supportedTypes, cType) {
		respondWithError(w, http.StatusBadRequest, "ill-formed, unsupported or missing content type", fmt.Errorf("ill-formed or missing content type"))
		return
	}

	parts := strings.Split(cType, "/")
	fileExt := parts[len(parts)-1]

	metadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't find video", err)
		return
	}
	if metadata.UserID != userID {
		respondWithError(w, http.StatusNotFound, "Unauthorized", fmt.Errorf("Unauthorized"))
		return
	}

	fileName := fmt.Sprintf("assets/%s.%s", metadata.ID, fileExt)
	file, err := os.Create(fileName)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "I/O error", err)
		return
	}
	_, err = io.Copy(file, data)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "I/O error", err)
		return
	}

	url := fmt.Sprintf("http://localhost:%v/%v", cfg.port, fileName)
	metadata.ThumbnailURL = &url
	err = cfg.db.UpdateVideo(metadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, metadata)
}
