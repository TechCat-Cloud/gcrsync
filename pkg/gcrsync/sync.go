/*
 * Copyright © 2018 mritd <mritd1234@gmail.com>
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
 * THE SOFTWARE.
 */

package gcrsync

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/sirupsen/logrus"

	"github.com/mritd/gcrsync/pkg/utils"
)

const (
	ChangeLog      = "CHANGELOG-%s.md"
	GcrRegistryTpl = "gcr.io/%s/%s"
	GcrImages      = "https://gcr.io/v2/%s/tags/list"
	GcrImageTags   = "https://gcr.io/v2/%s/%s/tags/list"
	DockerHubImage = "https://hub.docker.com/v2/repositories/%s/?page_size=100"
	DockerHubTags  = "https://hub.docker.com/v2/repositories/%s/%s/tags/?page_size=100"
)

func (g *Gcr) Sync() {

	gcrImages := g.gcrImageList()
	dockerHubImages := g.dockerHubImageList()
	needSyncImages := utils.SliceDiff(gcrImages, dockerHubImages)

	logrus.Infof("Google container registry images total: %d", len(gcrImages))
	logrus.Infof("Docker hub images total: %d", len(dockerHubImages))
	logrus.Infof("Number of images waiting to be processed: %d", len(needSyncImages))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if g.SyncTimeOut != 0 {
			select {
			case <-time.After(g.SyncTimeOut):
				cancel()
			}
		}
	}()

	processWg := new(sync.WaitGroup)
	processWg.Add(len(needSyncImages))

	for _, imageName := range needSyncImages {
		tmpImageName := imageName
		go func() {
			defer func() {
				g.ProcessLimit <- 1
				processWg.Done()
			}()
			select {
			case <-g.ProcessLimit:
				g.Process(tmpImageName)
			case <-ctx.Done():
			}
		}()
	}

	// doc gen
	chgWg := new(sync.WaitGroup)
	chgWg.Add(1)
	go func() {
		defer chgWg.Done()

		var images []string
		for {
			select {
			case imageName, ok := <-g.update:
				if ok {
					images = append(images, imageName)
				} else {
					goto ChangeLogDone
				}
			case <-ctx.Done():
				goto ChangeLogDone
			}
		}
	ChangeLogDone:
		if len(images) > 0 {
			g.Commit(images)
		}
	}()

	processWg.Wait()
	close(g.update)
	chgWg.Wait()

}

func (g *Gcr) Monitor() {

	if g.MonitorCount == -1 {
		for {
			select {
			case <-time.Tick(5 * time.Second):
				gcrImages := g.gcrImageList()
				dockerHubImages := g.dockerHubImageList()
				needSyncImages := utils.SliceDiff(gcrImages, dockerHubImages)
				logrus.Infof("Gcr images: %d | Docker hub images: %d | Waiting process: %d", len(gcrImages), len(dockerHubImages), len(needSyncImages))
			}
		}
	} else {
		for i := 0; i < g.MonitorCount; i++ {
			select {
			case <-time.Tick(5 * time.Second):
				gcrImages := g.gcrImageList()
				dockerHubImages := g.dockerHubImageList()
				needSyncImages := utils.SliceDiff(gcrImages, dockerHubImages)
				logrus.Infof("Gcr images: %d | Docker hub images: %d | Waiting process: %d", len(gcrImages), len(dockerHubImages), len(needSyncImages))
			}
		}
	}

}

func (g *Gcr) Init() {

	if g.Debug {
		logrus.SetLevel(logrus.DebugLevel)
	}

	logrus.Infoln("Init http client.")
	g.httpClient = &http.Client{
		Timeout: g.HttpTimeOut,
	}
	if g.Proxy != "" {
		p := func(_ *http.Request) (*url.URL, error) {
			return url.Parse(g.Proxy)
		}
		g.httpClient.Transport = &http.Transport{Proxy: p}
	}

	logrus.Infoln("Init docker client.")
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithVersion("1.39"))
	utils.CheckAndExit(err)
	g.dockerClient = dockerClient

	logrus.Infoln("Init limit channel.")
	for i := 0; i < cap(g.QueryLimit); i++ {
		g.QueryLimit <- 1
	}
	for i := 0; i < cap(g.ProcessLimit); i++ {
		g.ProcessLimit <- 1
	}

	logrus.Infoln("Init update channel.")
	g.update = make(chan string, 20)

	logrus.Infoln("Init commit repo.")
	if g.GithubToken == "" {
		utils.ErrorExit("Github Token is blank!", 1)
	}
	g.commitURL = "https://" + g.GithubToken + "@github.com/" + g.GithubRepo + ".git"
	g.Clone()

	logrus.Infoln("Init success...")
}
