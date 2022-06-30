package config

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

type ProjectList []Project

type Project struct {
	Url  string `json:"http_url_to_repo"`
	Name string `json:"name_with_namespace"`
}

type Gitlab struct {
	Key         string `json:"key"`
	Url         string `json:"url"`
	Membership  bool   `json:"membership-required"`
	Ownership   bool   `json:"ownership-required"`
	MaxProjects int    `json:"maximum-projects"`
	Template    *Repo  `json:"repo-options"`
}

// Set gitlab default options
func (g *Gitlab) UnmarshalJSON(data []byte) error {
	type gitlabDefaults Gitlab
	defaults := &gitlabDefaults{
		Membership:  true,
		Url:         "https://gitlab.com",
		MaxProjects: 1000,
	}

	err := json.Unmarshal(data, defaults)
	*g = Gitlab(*defaults)
	return err
}

// Retrieve a list of projects from the Gitlab API
func (g *Gitlab) GetProjects() (projects *ProjectList, err error) {
	projects = new(ProjectList)
	gitlabUrl := g.Url + "/api/v4/projects?simple=True"
	if g.Ownership {
		gitlabUrl = gitlabUrl + "&ownership=True"
	} else if g.Membership {
		gitlabUrl = gitlabUrl + "&membership=True"
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	for {
		req, err := http.NewRequest("GET", gitlabUrl, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+g.Key)

		r, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer r.Body.Close()

		if r.StatusCode != http.StatusOK {
			return nil, fmt.Errorf(
				"HTTP request failed for %s with status code %d\n",
				gitlabUrl,
				r.StatusCode)
		}

		pageProjects := new(ProjectList)
		json.NewDecoder(r.Body).Decode(pageProjects)
		newArray := append(*projects, *pageProjects...)
		projects = &newArray

		// No link header so no new page
		linkHeader := r.Header.Get("link")
		if linkHeader == "" {
			break
		}
		newLink := getLinkHeaderLink(linkHeader)
		if newLink == "" {
			break
		} else { // use url from link header to access next page
			gitlabUrl = newLink
		}
	}

	// Depending on the options users might try to index all public projects on
	// gitlab.com unintentionally. MaxProjects 0 = no limit
	if g.MaxProjects != 0 && len(*projects) > g.MaxProjects {
		return nil, fmt.Errorf(
			"Trying to clone %d projects which is larger than the maximum defined %d\n",
			len(*projects),
			g.MaxProjects)
	}

	return
}

// Get the URLs for all projects user has access to
func (g *Gitlab) GetProjectURLs() (URLs []string, err error) {
	projects, err := g.GetProjects()
	if err != nil {
		return nil, err
	}
	for _, proj := range *projects {
		URLs = append(URLs, proj.Url)
	}
	return URLs, nil
}

// Get projects from Gitlab API, create Repo structs for them
// and add them to the repos map. Options for the repos can be set
// under the "gitlab" and "repo-options" json keys
func (g *Gitlab) GetGitlabRepos(repos map[string]*Repo) {
	log.Println("Syncing in Gitlab projects")
	projects, err := g.GetProjects()
	if err != nil {
		log.Panicf("Failed to access Gitlab API: %v\n", err)
	}

	for _, proj := range *projects {
		if _, member := repos[proj.Name]; member {
			log.Printf(
				"Trying to add gitlab repo %s but there is already a defined config",
				" entry for %s, gitlab repo will be skipped", proj.Name, proj.Name)
			continue // do not risk overwriting manually added repo
		}
		if g.Template != nil {
			template := Repo(*g.Template)
			repos[proj.Name] = &template
		} else {
			repos[proj.Name] = &Repo{}
		}
		repos[proj.Name].Url = proj.Url
	}
}

// Dirty link header parser
// Example of input data:
// <https://gitlab.com/somelink>; rel="next", <https://gitlab.com/link2>; rel="first", <https://gitlab.com/link3>; rel="last"
func getLinkHeaderLink(header string) (link string) {
	rels := strings.Split(header, ", ")
	for _, rel := range rels {
		if !strings.HasSuffix(rel, "rel=\"next\"") {
			continue
		}
		rel = strings.TrimPrefix(rel, "<")
		rel = strings.TrimSuffix(rel, ">; rel=\"next\"")
		link = rel
	}
	return
}
