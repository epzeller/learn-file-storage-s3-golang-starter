package main

import (
	// "encoding/base64" // Need this package for Base64 encoding
	"fmt"
	"net/http"
	"crypto/rand"
	"io"
	"mime" // Required for parsing Content-Type to get the file extension
	"os"
	"path/filepath" // Required for safe path joining
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
	"encoding/base64"
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

	const maxMemory = 10 << 20
	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		// Use http.StatusInternalServerError for internal processing errors like form parsing
		respondWithError(w, http.StatusInternalServerError, "Couldn't parse form data", err)
		return
	}
	defer r.MultipartForm.RemoveAll() // Clean up uploaded files/memory

	// Get the image data from the form
	// Use r.FormFile to get the file data and file headers. Key is "thumbnail"
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		// http.StatusBadRequest is appropriate if the expected form field is missing
		respondWithError(w, http.StatusBadRequest, "Couldn't find 'thumbnail' file in form", err)
		return
	}
	defer file.Close()

	// Get the media type from the form file's Content-Type header
	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" {
		// Basic validation, although Content-Type should usually be present
		respondWithError(w, http.StatusBadRequest, "Missing Content-Type header", nil)
		return
	}

	// Read all the image data into a byte slice using io.ReadAll
	// imageData, err := io.ReadAll(file)
	// if err != nil {
	// 	respondWithError(w, http.StatusInternalServerError, "Couldn't read image data", err)
	// 	return
	// }

	// Get the video's metadata from the SQLite database
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		// Assuming GetVideo returns an error if not found or a database error occurs
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video metadata", err)
		return
	}

	// If the authenticated user is not the video owner, return a http.StatusUnauthorized response
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User is not the video owner", nil)
		return
	}

	// ------------------------------------------------------------------
	// --- 3. Save Bytes to File on Disk and Update Database URL ---
	// ------------------------------------------------------------------

	// Determine file extension from Content-Type
	// mime.TypeByExtension is unreliable. Use mime.ExtensionsByType if possible, or simple extraction.
	// We'll use mime.ExtensionsByType and take the first one, or fall back to ".jpg"
	var extension string
	extensions, err := mime.ExtensionsByType(mediaType)
	if err == nil && len(extensions) > 0 {
		extension = extensions[0] // e.g., ".png"
	} else {
		// Fallback for types not recognized or on error
		fmt.Fprintf(os.Stderr, "Warning: Could not determine extension for %s, defaulting to .jpg\n", mediaType)
		extension = ".jpg"
	}

	// Create a random string to use for the filename:
	// use crypto/rand.Read to fill a 32-byte slice with random bytes.
	rand_string := make([]byte, 4)
	n, err := rand.Read(rand_string)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error reading random bytes: %v", err)
	}
	if n != 4 {
		err_string := fmt.Sprintf("Expected to read 4 bytes, but read %d", n)
		respondWithError(w, http.StatusInternalServerError, err_string, err)
	}

	randomUint32 := base64.RawURLEncoding.EncodeToString(rand_string)
	filename := randomUint32 + extension
	//  Use base64.RawURLEncoding t
	// Create the unique file name: <videoID>.<file_extension>
	// filename := videoID.String() + extension
	
	// Create the full path to the new file: /assets/<videoID>.<file_extension>
	// cfg.assetsRoot is assumed to be the path to the 'assets' directory (e.g., "./assets")
	fullPath := filepath.Join(cfg.assetsRoot, filename)

	// Use os.Create to create the new file on disk
	outFile, err := os.Create(fullPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create file on disk", err)
		return
	}
	defer outFile.Close() // Close the output file

	// Copy the contents from the multipart.File (file) to the new file on disk (outFile)
	_, err = io.Copy(outFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy file contents", err)
		return
	}

	// Update the thumbnail_url to the new public path:
	// http://localhost:<port>/assets/<videoID>.<file_extension>
	// Use %s for cfg.port since it's a string
	thumbnailURL := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, filename)


	// // Convert the image data to a Base64 string
	// encodedData := base64.StdEncoding.EncodeToString(imageData)

	// // Create the data URL format: data:<media-type>;base64,<data>
	// dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, encodedData)

	// // Assign the address of the data URL string to the pointer field (*string)
	// video.ThumbnailURL = &dataURL

	// // Save the thumbnail to the global map
	// // Create a new thumbnail struct with the image data and media type
	// newThumbnail := thumbnail{
	// 	data:      imageData,
	// 	mediaType: mediaType,
	// }

	// // Add the thumbnail to the global map, using the video's ID as the key
	// videoThumbnails[videoID] = newThumbnail

	// // Update the video metadata so that it has a new thumbnail URL
	// // The thumbnail URL should have this format: http://localhost:<port>/api/thumbnails/{videoID}
	// thumbnailURL := fmt.Sprintf("http://localhost:%s/api/thumbnails/%s", cfg.port, videoID.String())
	video.ThumbnailURL = &thumbnailURL 

	// Update the record in the database by using the cfg.db.UpdateVideo function
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		// Log the error but continue to use the updated 'video' struct for response
		fmt.Printf("Warning: Failed to update video URL in DB: %v\n", err)
	}

	// Final response uses the 'video' struct we updated in memory
	respondWithJSON(w, http.StatusOK, video)

	// Gemini Code here
	// respondWithJSON(w, http.StatusOK, struct{}{})
}
