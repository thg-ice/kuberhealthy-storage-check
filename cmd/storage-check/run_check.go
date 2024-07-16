// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//     http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

// runStorageCheck sets up a storage PVC, a storage init and storage check and applies it to the cluster.
// Attempts to read a known values from the mounted PVC on each node
func runStorageCheck() {
	log.Infoln("Starting Storage check.")
	// Init a timeout for this entire check run.
	runTimeout := time.After(checkTimeLimit)
	// Init a timeout for cleaning up the check.  Assume that the check should not take more than 2m.
	cleanupTimeout := time.After(time.Minute * 2)

	// Delete all check resources (pvc, jobs) from this check that should not exist.
	select {
	case err := <-cleanUpOrphanedResources(ctx):
		// If the clean up completes with errors, we report those and stop the check cleanly.
		if err != nil {
			log.Errorln("error when cleaning up resources:", err)
			reportErrorsToKuberhealthy([]string{err.Error()})
			return
		}
		log.Infoln("Successfully cleaned up prior check resources.")
	case <-ctx.Done():
		// If there is a cancellation interrupt signal.
		log.Infoln("Canceling cleanup and shutting down from interrupt.")
		reportErrorsToKuberhealthy([]string{"failed to perform pre-check cleanup within timeout"})
		return
	case <-cleanupTimeout:
		// If the clean up took too long, exit.
		reportErrorsToKuberhealthy([]string{"failed to perform pre-check cleanup within timeout"})
		return
	}

	//TODO make all of this a function for easy reuse for  storage, deployment, service
	// Create a storage resource.
	storageConfig := createStorageConfig(checkStorageName, os.Getenv("CHECK_STORAGE_ACCESS_MODE"))
	log.Infoln("Created Storage resource.")
	log.Infof("It looks like: %v", storageConfig)

	// Apply the storage struct manifest to the cluster.
	var storageResult StorageResult
	select {
	case storageResult = <-createStorage(storageConfig):
		// Handle errors when the storage creation process completes.
		if storageResult.Err != nil {
			log.Errorln("error occurred creating storage in cluster:", storageResult.Err)
			reportErrorsToKuberhealthy([]string{storageResult.Err.Error()})
			return
		}
		// Continue with the check if there is no error.
		log.Infoln("Created storage in", storageResult.PersistentVolumeClaim.Namespace, "namespace:", storageResult.PersistentVolumeClaim.Name)
	case <-ctx.Done():
		// If there is a cancellation interrupt signal.
		log.Infoln("Cancelling storage creation job and shutting down due to interrupt.")
		reportErrorsToKuberhealthy([]string{"failed to create storage within timeout. Permissions?"})
		return
	case <-runTimeout:
		// If creating a storage took too long, exit.
		reportErrorsToKuberhealthy([]string{"failed to create storage within timeout. Permissions?"})
		return
	}

	//TODO make all of this a function for easy reuse for  storage, initstorage, deployment, service
	// Create a pod to initialize storage
	//initStorageConfig := initializeStorageConfig(checkStorageName+"-init", checkStorageName)
	initStorageConfig := initializeStorageConfig(checkStorageName+"-init-job", checkStorageName)
	log.Infoln("Created Storage Initialiazer resource.")
	log.Infof("It looks like: %v", initStorageConfig)

	// Initialize the storage
	var initStorageResult InitStorageResult
	select {
	case initStorageResult = <-initializeStorage(initStorageConfig):
		// Handle errors when the storage creation process completes.
		if initStorageResult.Err != nil {
			log.Errorln("error occurred initializing storage in cluster:", initStorageResult.Err)
			reportErrorsToKuberhealthy([]string{initStorageResult.Err.Error()})
			return
		}
		// Continue with the check if there is no error.
		log.Infoln("Initialized storage in", initStorageResult.Pod.Namespace, "namespace:", initStorageResult.Pod.Name)
	case <-ctx.Done():
		// If there is a cancellation interrupt signal.
		log.Infoln("Cancelling init storage job and shutting down due to interrupt.")
		reportErrorsToKuberhealthy([]string{"failed to initialize storage storage within timeout"})
		return
	case <-runTimeout:
		// If creating a storage took too long, exit.
		reportErrorsToKuberhealthy([]string{"failed to initialize storage within timeout"})
		return
	}
	config := checkNodeConfig(checkStorageName+"-check-job", checkStorageName)
	log.Infoln("Created config.")
	log.Infof("It looks like: %+v", config)

	// Initialize the storage
	var checkStorageResult CheckStorageResult
	select {
	case checkStorageResult = <-checkStorage(config):
		// Handle errors when the storage check process completes.
		if checkStorageResult.Err != nil {
			log.Errorln("error occurred checking storage in cluster:", checkStorageResult.Err)
			reportErrorsToKuberhealthy([]string{checkStorageResult.Err.Error()})
			return
		}
		// Continue with the check if there is no error.
		log.Infoln("Initialized storage check in namespace:", checkStorageResult.Pod.Namespace, "pod:", checkStorageResult.Pod.Name)
	case <-ctx.Done():
		// If there is a cancellation interrupt signal.
		log.Infoln("Cancelling storage check and shutting down due to interrupt.")
		reportErrorsToKuberhealthy([]string{"failed to check already initialized storage within timeout"})
		return
	case <-runTimeout:
		// If creating a storage took too long, exit.
		reportErrorsToKuberhealthy([]string{"failed to check already initialized storage within timeout"})
		return
	}
	// If we made it here then our Job returned ok and the storage check passed
	log.Infoln("Check for node was healthy! Removing the job now...")

	// Delete the storage check Job.
	// TODO - add select to catch context timeout expiration
	err := deleteStorageCheckAndWait(ctx)
	if err != nil {
		log.Errorln("error cleaning up storage check job:", err)
	}

	log.Infoln("Removed job check")

	// Clean up!
	cleanUpError := cleanUp(ctx)
	if cleanUpError != nil {
		reportErrorsToKuberhealthy([]string{cleanUpError.Error()})
	}
	// Report to Kuberhealthy.
	reportOKToKuberhealthy()
}

// cleanUp cleans up the deployment check and all resource manifests created that relate to
// the check.
// TODO - add in context that expires when check times out
func cleanUp(ctx context.Context) error {
	log.Infoln("Cleaning up storage, storage init job and storage check job.")
	var err error
	var resultErr error
	errorMessage := ""

	// Delete the storage check Job.
	// TODO - add select to catch context timeout expiration
	err = deleteStorageCheckAndWait(ctx)
	if err != nil {
		log.Errorln("error cleaning up storage check job:", err)
		if len(errorMessage) != 0 {
			errorMessage = errorMessage + " | "
		}
		errorMessage = errorMessage + "error cleaning up storage check job:" + err.Error()
	}

	// Delete the Storage init Job.
	// TODO - add select to catch context timeout expiration
	err = deleteStorageInitJobAndWait(ctx)
	if err != nil {
		log.Errorln("error cleaning up storage init:", err)
		if len(errorMessage) != 0 {
			errorMessage = errorMessage + " | "
		}
		errorMessage = errorMessage + "error cleaning up storage init:" + err.Error()
	}

	// Delete the storage.
	// TODO - add select to catch context timeout expiration
	err = deleteStorageAndWait(ctx)
	if err != nil {
		log.Errorln("error cleaning up storage:", err)
		if len(errorMessage) != 0 {
			errorMessage = errorMessage + " | "
		}
		errorMessage = errorMessage + "error cleaning up storage:" + err.Error()
	}

	log.Infoln("Finished clean up process.")

	// Create an error if errors occurred during the clean up process.
	if len(errorMessage) != 0 {
		resultErr = fmt.Errorf("%s", errorMessage)
	}

	return resultErr
}

// cleanUpOrphanedResources cleans up previous deployment and services and ensures
// a clean slate before beginning a deployment and service check.
func cleanUpOrphanedResources(ctx context.Context) chan error {

	cleanUpChan := make(chan error)

	go func(c context.Context) {
		log.Infoln("Wiping all found orphaned resources belonging to this check.")

		defer close(cleanUpChan)

		storageCheckExists, err := findPreviousStorageCheckJob()
		if err != nil {
			log.Warnln("Failed to find previous storage check:", err.Error())
		}
		if storageCheckExists {
			log.Infoln("Found previous storage check.")
		}

		storageInitExists, err := findPreviousStorageInitJob()
		if err != nil {
			log.Warnln("Failed to find previous Storage Init Job:", err.Error())
		}
		if storageInitExists {
			log.Infoln("Found previous storage storage init job.")
		}

		storageExists, err := findPreviousStorage()
		if err != nil {
			log.Warnln("Failed to find previous Storage:", err.Error())
		}
		if storageExists {
			log.Infoln("Found previous storage.")
		}

		if storageCheckExists || storageExists || storageInitExists {
			cleanUpChan <- cleanUp(c)
		} else {
			cleanUpChan <- nil
		}
	}(ctx)

	return cleanUpChan
}
