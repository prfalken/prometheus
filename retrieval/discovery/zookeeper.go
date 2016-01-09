package discovery

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/common/log"
	"github.com/samuel/go-zookeeper/zk"
)

type zookeeperLogger struct {
}

// Implements zk.Logger
func (zl zookeeperLogger) Printf(s string, i ...interface{}) {
	log.Infof(s, i...)
}

type zookeeperTreeCache struct {
	conn     *zk.Conn
	prefix   string
	events   chan zookeeperTreeCacheEvent
	zkEvents chan zk.Event
	stop     chan struct{}
	head     *zookeeperTreeCacheNode
}

type zookeeperTreeCacheEvent struct {
	Path string
	Data *[]byte
}

type zookeeperTreeCacheNode struct {
	data     *[]byte
	events   chan zk.Event
	done     chan struct{}
	stopped  bool
	children map[string]*zookeeperTreeCacheNode
}

func newZookeeperTreeCache(conn *zk.Conn, path string, events chan zookeeperTreeCacheEvent) *zookeeperTreeCache {
	tc := &zookeeperTreeCache{
		conn:   conn,
		prefix: path,
		events: events,
		stop:   make(chan struct{}),
	}
	tc.head = &zookeeperTreeCacheNode{
		events:   make(chan zk.Event),
		children: map[string]*zookeeperTreeCacheNode{},
		stopped:  true,
	}
	err := tc.recursiveNodeUpdate(path, tc.head)
	if err != nil {
		log.Errorf("Error during initial read of Zookeeper: %s", err)
	}
	go tc.loop(err != nil)
	return tc
}

func (tc *zookeeperTreeCache) Stop() {
	tc.stop <- struct{}{}
}

func (tc *zookeeperTreeCache) loop(failureMode bool) {
	retryChan := make(chan struct{})

	failure := func() {
		failureMode = true
		time.AfterFunc(time.Second*10, func() {
			retryChan <- struct{}{}
		})
	}
	if failureMode {
		failure()
	}

	for {
		select {
		case ev := <-tc.head.events:
			log.Debugf("Received Zookeeper event: %s", ev)
			if failureMode {
				continue
			}

			if ev.Type == zk.EventNotWatching {
				log.Infof("Lost connection to Zookeeper.")
				failure()
			} else {
				path := strings.TrimPrefix(ev.Path, tc.prefix)
				parts := strings.Split(path, "/")
				node := tc.head
				for _, part := range parts[1:] {
					childNode := node.children[part]
					if childNode == nil {
						childNode = &zookeeperTreeCacheNode{
							events:   tc.head.events,
							children: map[string]*zookeeperTreeCacheNode{},
							done:     make(chan struct{}, 1),
						}
						node.children[part] = childNode
					}
					node = childNode
				}

				err := tc.recursiveNodeUpdate(ev.Path, node)
				if err != nil {
					log.Errorf("Error during processing of Zookeeper event: %s", err)
					failure()
				} else if tc.head.data == nil {
					log.Errorf("Error during processing of Zookeeper event: path %s no longer exists", tc.prefix)
					failure()
				}
			}
		case <-retryChan:
			log.Infof("Attempting to resync state with Zookeeper")
			// Reset root child nodes before traversing the Zookeeper path.
			tc.head.children = make(map[string]*zookeeperTreeCacheNode)
			err := tc.recursiveNodeUpdate(tc.prefix, tc.head)
			if err != nil {
				log.Errorf("Error during Zookeeper resync: %s", err)
				failure()
			} else {
				log.Infof("Zookeeper resync successful")
				failureMode = false
			}
		case <-tc.stop:
			close(tc.events)
			return
		}
	}
}

func (tc *zookeeperTreeCache) recursiveNodeUpdate(path string, node *zookeeperTreeCacheNode) error {
	data, _, dataWatcher, err := tc.conn.GetW(path)
	if err == zk.ErrNoNode {
		tc.recursiveDelete(path, node)
		if node == tc.head {
			return fmt.Errorf("path %s does not exist", path)
		}
		return nil
	} else if err != nil {
		return err
	}

	if node.data == nil || !bytes.Equal(*node.data, data) {
		node.data = &data
		tc.events <- zookeeperTreeCacheEvent{Path: path, Data: node.data}
	}

	children, _, childWatcher, err := tc.conn.ChildrenW(path)
	if err == zk.ErrNoNode {
		tc.recursiveDelete(path, node)
		return nil
	} else if err != nil {
		return err
	}

	currentChildren := map[string]struct{}{}
	for _, child := range children {
		currentChildren[child] = struct{}{}
		childNode := node.children[child]
		// Does not already exists or we previous had a watch that
		// triggered.
		if childNode == nil || childNode.stopped {
			node.children[child] = &zookeeperTreeCacheNode{
				events:   node.events,
				children: map[string]*zookeeperTreeCacheNode{},
				done:     make(chan struct{}, 1),
			}
			err = tc.recursiveNodeUpdate(path+"/"+child, node.children[child])
			if err != nil {
				return err
			}
		}
	}

	// Remove nodes that no longer exist
	for name, childNode := range node.children {
		if _, ok := currentChildren[name]; !ok || node.data == nil {
			tc.recursiveDelete(path+"/"+name, childNode)
			delete(node.children, name)
		}
	}

	go func() {
		// Pass up zookeeper events, until the node is deleted.
		select {
		case event := <-dataWatcher:
			node.events <- event
		case event := <-childWatcher:
			node.events <- event
		case <-node.done:
		}
	}()
	return nil
}

func (tc *zookeeperTreeCache) recursiveDelete(path string, node *zookeeperTreeCacheNode) {
	if !node.stopped {
		node.done <- struct{}{}
		node.stopped = true
	}
	if node.data != nil {
		tc.events <- zookeeperTreeCacheEvent{Path: path, Data: nil}
		node.data = nil
	}
	for name, childNode := range node.children {
		tc.recursiveDelete(path+"/"+name, childNode)
	}
}
