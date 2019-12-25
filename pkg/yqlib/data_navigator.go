package yqlib

import (
	"fmt"
	"strconv"

	yaml "gopkg.in/yaml.v3"
)

type DataNavigator interface {
	Traverse(value *yaml.Node, path []string) error
}

type navigator struct {
	navigationSettings NavigationSettings
}

func NewDataNavigator(navigationSettings NavigationSettings) DataNavigator {
	return &navigator{
		navigationSettings: navigationSettings,
	}
}

func (n *navigator) Traverse(value *yaml.Node, path []string) error {
	realValue := value
	emptyArray := make([]interface{}, 0)
	if realValue.Kind == yaml.DocumentNode {
		log.Debugf("its a document! returning the first child")
		return n.doTraverse(value.Content[0], "", path, emptyArray)
	}
	return n.doTraverse(value, "", path, emptyArray)
}

func (n *navigator) doTraverse(value *yaml.Node, head string, path []string, pathStack []interface{}) error {
	if len(path) > 0 {
		log.Debugf("diving into %v", path[0])
		DebugNode(value)
		return n.recurse(value, path[0], path[1:], pathStack)
	}
	return n.navigationSettings.Visit(value, head, path, pathStack)
}

func (n *navigator) getOrReplace(original *yaml.Node, expectedKind yaml.Kind) *yaml.Node {
	if original.Kind != expectedKind {
		log.Debug("wanted %v but it was %v, overriding", expectedKind, original.Kind)
		return &yaml.Node{Kind: expectedKind}
	}
	return original
}

func (n *navigator) recurse(value *yaml.Node, head string, tail []string, pathStack []interface{}) error {
	log.Debug("recursing, processing %v", head)
	switch value.Kind {
	case yaml.MappingNode:
		log.Debug("its a map with %v entries", len(value.Content)/2)
		return n.recurseMap(value, head, tail, pathStack)
	case yaml.SequenceNode:
		log.Debug("its a sequence of %v things!, %v", len(value.Content))
		if head == "*" {
			return n.splatArray(value, tail, pathStack)
		} else if head == "+" {
			return n.appendArray(value, tail, pathStack)
		}
		return n.recurseArray(value, head, tail, pathStack)
	case yaml.AliasNode:
		log.Debug("its an alias!")
		DebugNode(value.Alias)
		if n.navigationSettings.FollowAlias(value, head, tail, pathStack) == true {
			log.Debug("following the alias")
			return n.recurse(value.Alias, head, tail, pathStack)
		}
		return nil
	default:
		return nil
	}
}

func (n *navigator) recurseMap(value *yaml.Node, head string, tail []string, pathStack []interface{}) error {
	visited, errorVisiting := n.visitMatchingEntries(value, head, tail, pathStack, func(contents []*yaml.Node, indexInMap int) error {
		contents[indexInMap+1] = n.getOrReplace(contents[indexInMap+1], guessKind(tail, contents[indexInMap+1].Kind))
		return n.doTraverse(contents[indexInMap+1], head, tail, append(pathStack, contents[indexInMap].Value))
	})

	if errorVisiting != nil {
		return errorVisiting
	}

	if visited || head == "*" || n.navigationSettings.AutoCreateMap(value, head, tail, pathStack) == false {
		return nil
	}

	mapEntryKey := yaml.Node{Value: head, Kind: yaml.ScalarNode}
	value.Content = append(value.Content, &mapEntryKey)
	mapEntryValue := yaml.Node{Kind: guessKind(tail, 0)}
	value.Content = append(value.Content, &mapEntryValue)
	log.Debug("adding new node %v", value.Content)
	return n.doTraverse(&mapEntryValue, head, tail, append(pathStack, head))
}

// need to pass the node in, as it may be aliased
type mapVisitorFn func(contents []*yaml.Node, index int) error

func (n *navigator) visitDirectMatchingEntries(node *yaml.Node, head string, tail []string, pathStack []interface{}, visit mapVisitorFn) (bool, error) {
	var contents = node.Content
	visited := false
	for index := 0; index < len(contents); index = index + 2 {
		content := contents[index]
		log.Debug("index %v, checking %v, %v", index, content.Value, content.Tag)

		if n.navigationSettings.ShouldVisit(content, head, tail, pathStack) == true {
			log.Debug("found a match! %v", content.Value)
			errorVisiting := visit(contents, index)
			if errorVisiting != nil {
				return visited, errorVisiting
			}
			visited = true
		}
	}
	return visited, nil
}

func (n *navigator) visitMatchingEntries(node *yaml.Node, head string, tail []string, pathStack []interface{}, visit mapVisitorFn) (bool, error) {
	var contents = node.Content
	log.Debug("visitMatchingEntries %v", head)
	DebugNode(node)
	// value.Content is a concatenated array of key, value,
	// so keys are in the even indexes, values in odd.
	// merge aliases are defined first, but we only want to traverse them
	// if we don't find a match directly on this node first.
	visited, errorVisitedDirectEntries := n.visitDirectMatchingEntries(node, head, tail, pathStack, visit)

	//TODO: crap we have to remember what we visited so we dont print the same key in the alias
	// eff

	if errorVisitedDirectEntries != nil || visited == true || n.navigationSettings.FollowAlias(node, head, tail, pathStack) == false {
		return visited, errorVisitedDirectEntries
	}
	// didnt find a match, lets check the aliases.

	return n.visitAliases(contents, head, tail, pathStack, visit)
}

func (n *navigator) visitAliases(contents []*yaml.Node, head string, tail []string, pathStack []interface{}, visit mapVisitorFn) (bool, error) {
	// merge aliases are defined first, but we only want to traverse them
	// if we don't find a match on this node first.
	// traverse them backwards so that the last alias overrides the preceding.
	// a node can either be
	// an alias to one other node (e.g. <<: *blah)
	// or a sequence of aliases   (e.g. <<: [*blah, *foo])
	log.Debug("checking for aliases")
	for index := len(contents) - 2; index >= 0; index = index - 2 {

		if contents[index+1].Kind == yaml.AliasNode {
			valueNode := contents[index+1]
			log.Debug("found an alias")
			DebugNode(contents[index])
			DebugNode(valueNode)

			visitedAlias, errorInAlias := n.visitMatchingEntries(valueNode.Alias, head, tail, pathStack, visit)
			if visitedAlias == true || errorInAlias != nil {
				return visitedAlias, errorInAlias
			}
		} else if contents[index+1].Kind == yaml.SequenceNode {
			// could be an array of aliases...
			visitedAliasSeq, errorVisitingAliasSeq := n.visitAliasSequence(contents[index+1].Content, head, tail, pathStack, visit)
			if visitedAliasSeq == true || errorVisitingAliasSeq != nil {
				return visitedAliasSeq, errorVisitingAliasSeq
			}
		}
	}
	log.Debug("nope no matching aliases found")
	return false, nil
}

func (n *navigator) visitAliasSequence(possibleAliasArray []*yaml.Node, head string, tail []string, pathStack []interface{}, visit mapVisitorFn) (bool, error) {
	// need to search this backwards too, so that aliases defined last override the preceding.
	for aliasIndex := len(possibleAliasArray) - 1; aliasIndex >= 0; aliasIndex = aliasIndex - 1 {
		child := possibleAliasArray[aliasIndex]
		if child.Kind == yaml.AliasNode {
			log.Debug("found an alias")
			DebugNode(child)
			visitedAlias, errorInAlias := n.visitMatchingEntries(child.Alias, head, tail, pathStack, visit)
			if visitedAlias == true || errorInAlias != nil {
				return visitedAlias, errorInAlias
			}
		}
	}
	return false, nil
}

func (n *navigator) splatArray(value *yaml.Node, tail []string, pathStack []interface{}) error {
	for index, childValue := range value.Content {
		log.Debug("processing")
		DebugNode(childValue)
		head := fmt.Sprintf("%v", index)
		childValue = n.getOrReplace(childValue, guessKind(tail, childValue.Kind))
		var err = n.doTraverse(childValue, head, tail, append(pathStack, index))
		if err != nil {
			return err
		}
	}
	return nil
}

func (n *navigator) appendArray(value *yaml.Node, tail []string, pathStack []interface{}) error {
	var newNode = yaml.Node{Kind: guessKind(tail, 0)}
	value.Content = append(value.Content, &newNode)
	log.Debug("appending a new node, %v", value.Content)
	head := fmt.Sprintf("%v", len(value.Content)-1)
	return n.doTraverse(&newNode, head, tail, append(pathStack, len(value.Content)-1))
}

func (n *navigator) recurseArray(value *yaml.Node, head string, tail []string, pathStack []interface{}) error {
	var index, err = strconv.ParseInt(head, 10, 64) // nolint
	if err != nil {
		return err
	}
	if index >= int64(len(value.Content)) {
		return nil
	}
	value.Content[index] = n.getOrReplace(value.Content[index], guessKind(tail, value.Content[index].Kind))
	return n.doTraverse(value.Content[index], head, tail, append(pathStack, index))
}
