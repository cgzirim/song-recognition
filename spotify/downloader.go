package spotify

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"song-recognition/shazam"
	"song-recognition/utils"
	"strconv"
	"strings"
	"sync"
	"time"

	// "song-recognition/youtube"

	"github.com/fatih/color"
	"github.com/kkdai/youtube/v2"
)

var yellow = color.New(color.FgYellow)

func DlSingleTrack(url, savePath string) error {
	trackInfo, err := TrackInfo(url)
	if err != nil {
		return err
	}

	fmt.Println("Getting track info...")
	time.Sleep(500 * time.Millisecond)
	track := []Track{*trackInfo}

	fmt.Println("Now, downloading track...")
	_, err = dlTrack(track, savePath)
	if err != nil {
		return err
	}

	return nil
}

func DlPlaylist(url, savePath string) (int, error) {
	tracks, err := PlaylistInfo(url)
	if err != nil {
		return 0, err
	}

	time.Sleep(1 * time.Second)
	fmt.Println("Now, downloading playlist...")
	totalTracksDownloaded, err := dlTrack(tracks, savePath)
	if err != nil {
		fmt.Println(err)
		return 0, err
	}

	return totalTracksDownloaded, nil
}

func DlAlbum(url, savePath string) (int, error) {
	tracks, err := AlbumInfo(url)
	if err != nil {
		return 0, err
	}

	time.Sleep(1 * time.Second)
	fmt.Println("Now, downloading album...")
	totalTracksDownloaded, err := dlTrack(tracks, savePath)
	if err != nil {
		return 0, err
	}

	return totalTracksDownloaded, nil
}

func dlTrack(tracks []Track, path string) (int, error) {
	var wg sync.WaitGroup
	var downloadedTracks []string
	var totalTracks int
	results := make(chan int, len(tracks))
	numCPUs := runtime.NumCPU()
	semaphore := make(chan struct{}, numCPUs)

	for _, t := range tracks {
		wg.Add(1)
		go func(track Track) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() {
				<-semaphore
			}()

			trackCopy := &Track{
				Album:    track.Album,
				Artist:   track.Artist,
				Artists:  track.Artists,
				Duration: track.Duration,
				Title:    track.Title,
			}

			// id1, err := VideoID(*trackCopy)
			ytID, err := GetYoutubeId(*trackCopy)
			if ytID == "" || err != nil {
				yellow.Printf("Error (1): '%s' by '%s' could not be downloaded\n", trackCopy.Title, trackCopy.Artist)
				return
			}

			trackCopy.Title, trackCopy.Artist = correctFilename(trackCopy.Title, trackCopy.Artist)
			err = getAudio(ytID, path, trackCopy.Title, trackCopy.Artist)
			if err != nil {
				yellow.Printf("Error (2): '%s' by '%s' could not be downloaded: %s\n", trackCopy.Title, trackCopy.Artist, err)
				return
			}
			// Process and save audio
			filename := fmt.Sprintf("%s - %s.m4a", trackCopy.Title, trackCopy.Artist)
			route := filepath.Join(path, filename)
			err = processAndSaveSong(route, trackCopy.Title, trackCopy.Artist, ytID)
			if err != nil {
				yellow.Println("Error processing audio: ", err)
			}

			trackCopy.Title, trackCopy.Artist = correctFilename(trackCopy.Title, trackCopy.Artist)
			filePath := fmt.Sprintf("%s%s - %s.m4a", path, trackCopy.Title, trackCopy.Artist)

			if err := addTags(filePath, *trackCopy); err != nil {
				yellow.Println("Error adding tags: ", filePath)
				return
			}

			size, _ := GetFileSize(filePath)
			if size < 1 {
				DeleteFile(filePath)
			}

			fmt.Printf("'%s' by '%s' was downloaded\n", track.Title, track.Artist)
			downloadedTracks = append(downloadedTracks, fmt.Sprintf("%s, %s", track.Title, track.Artist))
			results <- 1
		}(t)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for range results {
		totalTracks++
	}

	fmt.Println("Total tracks downloaded:", totalTracks)
	return totalTracks, nil

}

/* github.com/kkdai/youtube */
func getAudio(id, path, title, artist string) error {
	dir, err := os.Stat(path)
	if err != nil {
		panic(err)
	}

	if !dir.IsDir() {
		return errors.New("the path is not valid (not a dir)")
	}

	db, err := utils.NewDbClient()
	if err != nil {
		return fmt.Errorf("error connecting to DB: %d", err)
	}
	defer db.Close()

	// Check if the song has been processed and saved before
	songKey := fmt.Sprintf("%s - %s", title, artist)
	songExists, err := db.SongExists(songKey)
	if err != nil {
		return err
	}
	if songExists {
		return fmt.Errorf("song exists")
	}

	client := youtube.Client{}
	video, err := client.GetVideo(id)
	if err != nil {
		return err
	}

	/* itag code: 140, container: m4a, content: audio, bitrate: 128k */
	/* change the FindByItag parameter to 139 if you want smaller files (but with a bitrate of 48k) */
	formats := video.Formats.Itag(140)

	filename := fmt.Sprintf("%s - %s.m4a", title, artist)
	route := filepath.Join(path, filename)

	/* in some cases, when attempting to download the audio
	using the library github.com/kkdai/youtube,
	the download fails (and shows the file size as 0 bytes)
	until the second or third attempt. */
	var fileSize int64
	file, err := os.Create(route)
	if err != nil {
		return err
	}

	for fileSize == 0 {
		stream, _, err := client.GetStream(video, &formats[0])
		if err != nil {
			return err
		}

		if _, err = io.Copy(file, stream); err != nil {
			return err
		}

		fileSize, _ = GetFileSize(route)
	}
	defer file.Close()

	return nil
}

func saveAudioToFile(audioReader io.Reader, path, title, artist string) error {
	dir, err := os.Stat(path)
	if err != nil {
		panic(err)
	}

	if !dir.IsDir() {
		return errors.New("the path is not valid (not a dir)")
	}

	filename := fmt.Sprintf("%s - %s.m4a", title, artist)
	route := filepath.Join(path, filename)

	/* in some cases, when attempting to download the audio
	using the library github.com/kkdai/youtube,
	the download fails (and shows the file size as 0 bytes)
	until the second or third attempt. */
	file, err := os.Create(route)
	if err != nil {
		return err
	}

	defer file.Close()

	// Copy the audio stream to the file
	_, err = io.Copy(file, audioReader)
	if err != nil {
		return err
	}

	return nil
}

func addTags(file string, track Track) error {
	tempFile := file
	index := strings.Index(file, ".m4a")
	if index != -1 {
		result := tempFile[:index]       /* filename but with no extension ('/path/to/title - artist') */
		tempFile = result + "2" + ".m4a" /* just a temporary dumb name ('/path/to/title - artist2.m4a') */
	}

	cmd := exec.Command(
		"ffmpeg",
		"-i", file, /* /path/to/title - artist.m4a */
		"-c", "copy",
		"-metadata", fmt.Sprintf("album_artist=%s", track.Artist),
		"-metadata", fmt.Sprintf("title=%s", track.Title),
		"-metadata", fmt.Sprintf("artist=%s", track.Artist),
		"-metadata", fmt.Sprintf("album=%s", track.Album),
		tempFile, /* /path/to/title - artist2.m4a */
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println("ERROR FROM CMD:", err)
		fmt.Println("FFMPEG Output:", string(out))
		return err
	}
	// if err := cmd.Run(); err != nil {
	// 	fmt.Println("ERROR FROM CMD: ", err)
	// 	return err
	// }

	/* removes '2' from file name */
	if err := os.Rename(tempFile, file); err != nil {
		return err
	}

	return nil
}

/* fixes some invalid file names (windows is the capricious one) */
func correctFilename(title, artist string) (string, string) {
	if runtime.GOOS == "windows" {
		invalidChars := []byte{'<', '>', '<', ':', '"', '\\', '/', '|', '?', '*'}
		for _, invalidChar := range invalidChars {
			title = strings.ReplaceAll(title, string(invalidChar), "")
			artist = strings.ReplaceAll(artist, string(invalidChar), "")
		}
	} else {
		title = strings.ReplaceAll(title, "/", "\\")
		artist = strings.ReplaceAll(artist, "/", "\\")
	}

	return title, artist
}

func processAndSaveSong(m4aFile, songTitle, songArtist, ytID string) error {
	db, err := utils.NewDbClient()
	if err != nil {
		return fmt.Errorf("error connecting to DB: %d", err)
	}
	defer db.Close()

	// Check if the song has been processed and saved before
	songKey := fmt.Sprintf("%s - %s", songTitle, songArtist)
	songExists, err := db.SongExists(songKey)
	if err != nil {
		return fmt.Errorf("error checking if song exists: %v", err)
	}
	if songExists {
		fmt.Println("Song exists: ", songKey)
		return nil
	}

	// Convert M4A file to mono
	m4aFileMono := strings.TrimSuffix(m4aFile, filepath.Ext(m4aFile)) + "_mono.m4a"
	// defer os.Remove(m4aFileMono)
	audioBytes, err := ConvertM4aToMono(m4aFile, m4aFileMono)
	if err != nil {
		return fmt.Errorf("error converting M4A file to mono: %v", err)
	}

	// Run ffprobe to get metadata of the input file
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=bit_depth,sample_rate", "-of", "default=noprint_wrappers=1:nokey=1", m4aFileMono)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("error running ffprobe: %v", err)
	}

	// Parse the output to extract bit depth and sampling rate
	lines := strings.Split(string(output), "\n")
	// bitDepth, _ := strconv.Atoi(strings.TrimSpace(lines[1]))
	sampleRate, _ := strconv.Atoi(strings.TrimSpace(lines[0]))
	fmt.Printf("SAMPLE RATE for %s: %v", songTitle, sampleRate)

	chunkTag := shazam.ChunkTag{
		SongTitle:  songTitle,
		SongArtist: songArtist,
		YouTubeID:  ytID,
	}

	// Calculate fingerprints
	chunks := shazam.Chunkify(audioBytes)
	_, fingerprints := shazam.FingerprintChunks(chunks, &chunkTag)

	// Save fingerprints to MongoDB
	for fgp, ctag := range fingerprints {
		err := db.InsertChunkTag(fgp, ctag)
		if err != nil {
			return fmt.Errorf("error inserting document: %v", err)
		}
	}

	// Save the song as processed
	err = db.RegisterSong(songKey)
	if err != nil {
		return err
	}

	fmt.Println("Fingerprints saved to MongoDB successfully")
	return nil
}
