// Create handler

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gofrs/uuid"
	"github.com/julienschmidt/httprouter"
	"github.com/printmaps/printmaps/pd"
)

/*
createMetadata creates the meta data for a new map.
*/
func createMetadata(writer http.ResponseWriter, request *http.Request, _ httprouter.Params) {
	var pmErrorList pd.PrintmapsErrorList
	var pmData pd.PrintmapsData
	var pmState pd.PrintmapsState

	verifyContentType(request, &pmErrorList)
	verifyAccept(request, &pmErrorList)

	// process body
	if err := json.NewDecoder(request.Body).Decode(&pmData); err != nil {
		appendError(&pmErrorList, "2001", "error = "+err.Error(), "")
	} else {
		verifyMetadata(pmData, &pmErrorList)
	}

	if len(pmErrorList.Errors) == 0 {
		// request ok, response with (new) ID and data, persist data
		universallyUniqueIdentifier, err := uuid.NewV4()
		if err != nil {
			message := fmt.Sprintf("error <%v> at uuid.NewV4()", err)
			http.Error(writer, message, http.StatusInternalServerError)
			log.Printf("Response %d - %s", http.StatusInternalServerError, message)
			return
		}
		pmData.Data.ID = universallyUniqueIdentifier.String()

		if err := pd.WriteMetadata(pmData); err != nil {
			message := fmt.Sprintf("error <%v> at writeMetadata()", err)
			http.Error(writer, message, http.StatusInternalServerError)
			log.Printf("Response %d - %s", http.StatusInternalServerError, message)
			return
		}

		content, err := json.MarshalIndent(pmData, pd.IndentPrefix, pd.IndexString)
		if err != nil {
			message := fmt.Sprintf("error <%v> at son.MarshalIndent()", err)
			http.Error(writer, message, http.StatusInternalServerError)
			log.Printf("Response %d - %s", http.StatusInternalServerError, message)
			return
		}

		// write state
		pmState.Data.Type = "maps"
		pmState.Data.ID = pmData.Data.ID
		pmState.Data.Attributes.MapMetadataWritten = time.Now().Format(time.RFC3339)
		if err = pd.WriteMapstate(pmState); err != nil {
			message := fmt.Sprintf("error <%v> at updateMapstate()", err)
			http.Error(writer, message, http.StatusInternalServerError)
			log.Printf("Response %d - %s", http.StatusInternalServerError, message)
			return
		}

		writer.Header().Set("Content-Type", pd.JSONAPIMediaType)
		writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
		writer.WriteHeader(http.StatusCreated)
		writer.Write(content)
	} else {
		// request not ok, response with error list
		content, err := json.MarshalIndent(pmErrorList, pd.IndentPrefix, pd.IndexString)
		if err != nil {
			message := fmt.Sprintf("error <%v> at json.MarshalIndent()", err)
			http.Error(writer, message, http.StatusInternalServerError)
			log.Printf("Response %d - %s", http.StatusInternalServerError, message)
			return
		}

		writer.Header().Set("Content-Type", pd.JSONAPIMediaType)
		writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
		writer.WriteHeader(http.StatusBadRequest)
		writer.Write(content)
	}
}

/*
createMapfile creates a (asynchronous) build order for the map defined in the metadata.
*/
func createMapfile(writer http.ResponseWriter, request *http.Request, _ httprouter.Params) {
	var pmErrorList pd.PrintmapsErrorList
	var pmDataPost pd.PrintmapsData
	var pmData pd.PrintmapsData
	var pmState pd.PrintmapsState

	verifyContentType(request, &pmErrorList)
	verifyAccept(request, &pmErrorList)

	// process body (with map ID)
	err := json.NewDecoder(request.Body).Decode(&pmDataPost)
	if err != nil {
		appendError(&pmErrorList, "2001", "error = "+err.Error(), "")
	}

	id := pmDataPost.Data.ID

	// verify ID
	_, err = uuid.FromString(id)
	if err != nil {
		appendError(&pmErrorList, "4001", "error = "+err.Error(), "")
	}

	// map directory must exist
	if len(pmErrorList.Errors) == 0 {
		if !pd.IsExistMapDirectory(id) {
			appendError(&pmErrorList, "4002", "requested ID not found: "+id, id)
		}
	}

	if len(pmErrorList.Errors) == 0 {
		// request ok, read meta data from file
		if err := pd.ReadMetadata(&pmData, id); err != nil {
			if os.IsNotExist(err) {
				appendError(&pmErrorList, "4002", "requested ID not found: "+id, id)
			} else {
				message := fmt.Sprintf("error <%v> at readMetadata(), id = <%s>", err, id)
				http.Error(writer, message, http.StatusInternalServerError)
				log.Printf("Response %d - %s", http.StatusInternalServerError, message)
				return
			}
		}
		// verify required data
		verifyRequiredMetadata(pmData, &pmErrorList)
		if len(pmErrorList.Errors) == 0 {
			// everything is ok, create build order
			if err := createMapOrder(pmData); err != nil {
				message := fmt.Sprintf("error <%v> at createMapOrder(), id = <%s>", err, id)
				http.Error(writer, message, http.StatusInternalServerError)
				log.Printf("Response %d - %s", http.StatusInternalServerError, message)
				return
			}

			// read state
			if err := pd.ReadMapstate(&pmState, id); err != nil {
				if !os.IsNotExist(err) {
					message := fmt.Sprintf("error <%v> at readMapstate(), id = <%s>", err, id)
					http.Error(writer, message, http.StatusInternalServerError)
					log.Printf("Response %d - %s", http.StatusInternalServerError, message)
					return
				}
			}

			// write (update) state
			pmState.Data.Attributes.MapOrderSubmitted = time.Now().Format(time.RFC3339)
			pmState.Data.Attributes.MapBuildStarted = ""
			pmState.Data.Attributes.MapBuildCompleted = ""
			pmState.Data.Attributes.MapBuildSuccessful = ""
			pmState.Data.Attributes.MapBuildMessage = ""
			pmState.Data.Attributes.MapBuildBoxMillimeter = pd.BoxMillimeter{}
			pmState.Data.Attributes.MapBuildBoxPixel = pd.BoxPixel{}
			pmState.Data.Attributes.MapBuildBoxProjection = pd.BoxProjection{}
			pmState.Data.Attributes.MapBuildBoxWGS84 = pd.BoxWGS84{}
			if err = pd.WriteMapstate(pmState); err != nil {
				message := fmt.Sprintf("error <%v> at updateMapstate()", err)
				http.Error(writer, message, http.StatusInternalServerError)
				log.Printf("Response %d - %s", http.StatusInternalServerError, message)
				return
			}
		}
	}

	if len(pmErrorList.Errors) == 0 {
		// request ok, response with data
		content, err := json.MarshalIndent(pmData, pd.IndentPrefix, pd.IndexString)
		if err != nil {
			message := fmt.Sprintf("error <%v> at json.MarshalIndent()", err)
			http.Error(writer, message, http.StatusInternalServerError)
			log.Printf("Response %d - %s", http.StatusInternalServerError, message)
			return
		}

		writer.Header().Set("Content-Type", pd.JSONAPIMediaType)
		writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
		writer.WriteHeader(http.StatusAccepted)
		writer.Write(content)
	} else {
		// request not ok, response with error list
		content, err := json.MarshalIndent(pmErrorList, pd.IndentPrefix, pd.IndexString)
		if err != nil {
			message := fmt.Sprintf("error <%v> at json.MarshalIndent()", err)
			http.Error(writer, message, http.StatusInternalServerError)
			log.Printf("Response %d - %s", http.StatusInternalServerError, message)
			return
		}

		writer.Header().Set("Content-Type", pd.JSONAPIMediaType)
		writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
		writer.WriteHeader(http.StatusBadRequest)
		writer.Write(content)
	}
}
