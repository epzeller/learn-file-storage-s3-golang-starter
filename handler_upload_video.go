package main

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"github.com/google/uuid"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/aws" // Needed for aws.String()
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	// "github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	// Assuming respondWithError and respondWithJSON are defined globally
)

// NOTE: This implementation builds upon the setup and validation logic 
// from the previous steps.

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID format", err)
		return
	}

	// 1. Authentication and Authorization (from previous steps)
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

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video metadata", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User is not the video owner", nil)
		return
	}

	// 2. MaxBytesReader and FormFile (from previous steps)
	const maxUploadSize = 1 << 30 // 1GB
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	file, header, err := r.FormFile("video")
	if err != nil {
		if err.Error() == "http: request body too large" {
			respondWithError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("Video file exceeds the limit of %d bytes", maxUploadSize), nil)
			return
		}
		respondWithError(w, http.StatusBadRequest, "Couldn't read 'video' file from form", err)
		return
	}
	defer file.Close()

	// 3. MIME Type Validation (from previous steps)
	mediaType := header.Header.Get("Content-Type")
	baseType, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type header format", err)
		return
	}

	const expectedMIMEType = "video/mp4"
	if baseType != expectedMIMEType {
		respondWithError(w, http.StatusBadRequest,
			fmt.Sprintf("Invalid video format: expected %s, got %s", expectedMIMEType, baseType), nil)
		return
	}
	// Determine extension for S3 key
	extension := ".mp4" 

	// ------------------------------------------------------------------
	// --- Temporary File Handling and S3 Upload ---
	// ------------------------------------------------------------------

	// 4. Create a temporary file on disk
	// Pass an empty string for the directory to use the system default
	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4") 
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create temporary file", err)
		return
	}
	
	// defer remove the temp file
	defer os.Remove(tempFile.Name())
	
	// defer close the temp file (LIFO ensures it closes before removal)
	defer tempFile.Close()

	// 5. io.Copy the contents over from the wire (file) to the temp file
	_, err = io.Copy(tempFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to copy file contents to temporary storage", err)
		return
	}

	// 6. Reset the tempFile's file pointer to the beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to reset file pointer", err)
		return
	}
	
	// 7. Put the object into S3 using PutObject
	s3Key := videoID.String() + extension // Use <videoID>.mp4 as the key

	s3UploadInput := &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(s3Key),
		Body:        tempFile, // tempFile implements io.Reader
		ContentLength: aws.Int64(header.Size),
		ContentType: aws.String(baseType), // The validated MIME type
	}

	_, err = cfg.s3Client.PutObject(context.Background(), s3UploadInput)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload video to S3", err)
		return
	}
	
	// ------------------------------------------------------------------
	// --- Update Database URL and Respond ---
	// ------------------------------------------------------------------

	// 8. Update the VideoURL of the video record in the database
	// S3 URL format: https://<bucket-name>.s3.<region>.amazonaws.com/<key>
	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, s3Key)

	// Assign the address of the URL string to the pointer field
	video.VideoURL = &videoURL

	// Update the record in the database
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to update video URL in DB: %v\n", err)
	}

	respondWithJSON(w, http.StatusOK, video)
}