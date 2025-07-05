package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	http.MaxBytesReader(w, r.Body, 1 << 30)

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

	metadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't find video", err)
		return
	}
	if metadata.UserID != userID {
		respondWithError(w, http.StatusNotFound, "Unauthorized", fmt.Errorf("Unauthorized"))
		return
	}

	data, head, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Ill-formed request", err)
	}
	defer data.Close()

	cType, _, _ := mime.ParseMediaType(head.Header.Get("Content-Type"))
	supportedTypes := []string{"video/mp4"}
	if !slices.Contains(supportedTypes, cType) {
		respondWithError(w, http.StatusBadRequest, "ill-formed, unsupported or missing content type", fmt.Errorf("ill-formed or missing content type"))
		return
	}

	temp, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error while creating temp file", fmt.Errorf("error while creating temp file"))
		return
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	_, err = io.Copy(temp, data)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error while writing content to temp file", fmt.Errorf("error while writing content to temp file"))
		return
	}

	_, err = temp.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error while resetting temp file seek position", fmt.Errorf("error while resetting temp file seek position"))
		return
	}

	rnd := make([]byte, 32)
	rand.Read(rnd)
	buf := strings.Builder{}
	encoder := base64.NewEncoder(base64.RawURLEncoding, &buf)
	encoder.Write(rnd)
	encoder.Close()
	key := fmt.Sprintf("%s.mp4", buf.String())

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{Bucket: &cfg.s3Bucket, Key: &key, Body: temp, ContentType: aws.String("video/mp4")})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error while upload file to S3 bucket", fmt.Errorf("error while upload file to S3 bucket"))
		return
	}

	url := fmt.Sprintf("https://%v.s3.%v.amazonaws.com/%v", cfg.s3Bucket, cfg.s3Region, key)
	metadata.VideoURL = &url
	err = cfg.db.UpdateVideo(metadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, metadata)
}
