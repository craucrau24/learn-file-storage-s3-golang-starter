package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func getRatio(w int, h int) (float64, error) {
	if (h == 0) {
		return math.NaN(), fmt.Errorf("cannot divide by zero")
	}
	return float64(w) / float64(h), nil
}

func floatsEqual(f1 float64, f2 float64) bool {
	return math.Abs(f1 - f2) <= 0.01
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var buf bytes.Buffer;
	cmd.Stdout = &buf;
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("error while getting video format: %w", err)
	}

	type streamData struct {
		Width int  `json:"width"`
		Height int `json:"height"`
	}

	type videoDataInfo struct {
		Streams []streamData `json:"streams"`
	}
	var videoData videoDataInfo
	err = json.Unmarshal(buf.Bytes(), &videoData)
	if err != nil {
		return "", fmt.Errorf("error while parsing JSON from video format command: %w", err)
	}

	ratio, err := getRatio(videoData.Streams[0].Width, videoData.Streams[0].Height)
	if err != nil {
		return "", fmt.Errorf("error while computing ratio: %w", err)
	}

	landRatio, _ := getRatio(16, 9)
	portRatio, _ := getRatio(9, 16)

	var ratioStr string
	if floatsEqual(ratio, landRatio) {
		ratioStr = "landscape"
	} else if floatsEqual(ratio, portRatio) {
		ratioStr = "portrait"
	} else {
		ratioStr = "other"
	}
	return ratioStr, nil
}

func processVideoForFastStart(filePath string) (string, error) {
	output := filePath + ".processing"
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", output)
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("error while processing video for faststart: %w", err)
	}
	return output, nil
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	http.MaxBytesReader(w, r.Body, 1 << 30)

	// Retrieve videoID parameter
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// Authentication
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

	// Retrieve video from DB
	metadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't find video", err)
		return
	}
	// Check logged in user is the owner
	if metadata.UserID != userID {
		respondWithError(w, http.StatusNotFound, "Unauthorized", fmt.Errorf("Unauthorized"))
		return
	}

	// Retrieve video data from request
	data, head, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Ill-formed request", err)
	}
	defer data.Close()

	// Check content type
	cType, _, _ := mime.ParseMediaType(head.Header.Get("Content-Type"))
	supportedTypes := []string{"video/mp4"}
	if !slices.Contains(supportedTypes, cType) {
		respondWithError(w, http.StatusBadRequest, "ill-formed, unsupported or missing content type", fmt.Errorf("ill-formed or missing content type"))
		return
	}

	// Create temporary file and writing data into it
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

	// Get aspect ratio
	aspect, err := getVideoAspectRatio(temp.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error while trying to determine aspect ration of uploaded video", fmt.Errorf("error while trying to determine aspect ration of uploaded video"))
		return
	}

	// Process video file
	output, err := processVideoForFastStart(temp.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error while processing video file for fast start", fmt.Errorf("error while processing video file for fast start"))
		return
	}

	outputFile, err := os.Open(output)
	defer os.Remove(outputFile.Name())
	defer outputFile.Close()
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error while opening processed file", fmt.Errorf("error while opening processed file"))
		return
	}

	// Generate 32-byte length random string
	rnd := make([]byte, 32)
	rand.Read(rnd)
	buf := strings.Builder{}
	encoder := base64.NewEncoder(base64.RawURLEncoding, &buf)
	encoder.Write(rnd)
	encoder.Close()
	
	// Craft S3 key with aspect and previous random string
	key := fmt.Sprintf("%v/%s.mp4", aspect, buf.String())

	// Upload to S3 bucket
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{Bucket: &cfg.s3Bucket, Key: &key, Body: outputFile, ContentType: aws.String("video/mp4")})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error while upload file to S3 bucket", fmt.Errorf("error while upload file to S3 bucket"))
		return
	}

	// Update video metadata with AWS S3 URL
	url := fmt.Sprintf("%v,%v", cfg.s3Bucket, key)
	metadata.VideoURL = &url
	err = cfg.db.UpdateVideo(metadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't update video", err)
		return
	}

	// Respond with OK status
	respondWithJSON(w, http.StatusOK, metadata)
}
