package prow

/*
var uriNotFoundError = errors.New("URI not found")


func (job *Job) StartStop() (time.Time, time.Time, error) {
	var zero time.Time

	started, err := time.Parse(time.RFC3339, job.Status.StartTime)
	if err != nil {
		return zero, zero, fmt.Errorf("prow job %s #%s had invalid 'startTime': %s", job.Spec.Job, job.Status.BuildID, err)
	}

	var finished time.Time
	if job.Status.CompletionTime != "" {
		finished, err = time.Parse(time.RFC3339, job.Status.CompletionTime)
		if err != nil {
			return zero, zero, fmt.Errorf("prow job %s #%s had invalid 'completionTime': %s", job.Spec.Job, job.Status.BuildID, err)
		}
	}

	return started, finished, nil
}

func fetchJob(client *http.Client, job *Job, toDir string, jobURIPrefix *url.URL, artifactURIPrefix *url.URL, deckURI *url.URL) error {
	_, stop, err := job.StartStop()
	if err != nil {
		return err
	}

	logPath := job.Status.URL
	if !strings.HasPrefix(logPath, jobURIPrefix.String()) {
		return fmt.Errorf("prow job %s %s had invalid URL: %s", job.Spec.Job, job.Status.BuildID, logPath)
	}
	logPath = path.Join(strings.TrimPrefix(logPath, jobURIPrefix.String()), "build-log.txt")

	uris := make([]*url.URL, 0, 2)
	if artifactURIPrefix != nil {
		uris = append(uris, artifactURIPrefix.ResolveReference(&url.URL{Path: logPath}))
	}

	if deckURI != nil {
		uri := *deckURI
		uri.Path = "/log"
		query := url.Values{"id": []string{job.Status.BuildID}, "job": []string{job.Spec.Job}}
		uri.RawQuery = query.Encode()
		uris = append(uris, &uri)
	}

	if len(uris) == 0 {
		return fmt.Errorf("either the artifact-URI prefix or the deck URI must be set")
	}

	pathOnDisk := filepath.Join(toDir, filepath.FromSlash(logPath))
	errs := []error{}
	for _, uri := range uris {
		err = fetchArtifact(client, uri, pathOnDisk, stop)
		if err == nil {
			break
		} else if err != uriNotFoundError {
			errs = append(errs, err)
		}
	}
	return utilerrors.NewAggregate(errs)
}

func fetchArtifact(client *http.Client, uri *url.URL, path string, date time.Time) error {
	defer klog.V(4).Infof("Fetch %s to %s", uri, path)
	resp, err := client.Get(uri.String())
	if err != nil {
		return fmt.Errorf("unable to fetch artifact: %v", err)
	}
	defer func() {
		// ensure we pull the body completely so connections are reused
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == 404 {
			return uriNotFoundError
		}
		return fmt.Errorf("unable to fetch artifact %s: %d %s", uri.String(), resp.StatusCode, resp.Status)
	}

	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0777); err != nil {
		return fmt.Errorf("unable to create directory for artifact: %v", err)
	}

	f, err := os.OpenFile(path, os.O_EXCL|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("unable to fetch artifact, could not create log file: %v", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("unable to fetch artifact, could not copy log file: %v", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("unable to fetch artifact, could not close log file: %v", err)
	}

	if err := os.Chtimes(path, date, date); err != nil {
		return fmt.Errorf("unable to set file time while indexing to disk: %v", err)
	}

	return nil
}
*/
