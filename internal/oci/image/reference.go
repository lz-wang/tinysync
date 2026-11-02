package image

import "strings"

// ImageReference represents a parsed image reference
type ImageReference struct {
	OriginalRef string
	Registry    string
	Namespace   string
	RepoName    string
	Tag         string
}

func ParseReference(ref string) *ImageReference {
	return parseReference(ref, true)
}

func ParseRepository(ref string) *ImageReference {
	return parseReference(ref, false)
}

func (r *ImageReference) FullRepoName() string {
	if r == nil {
		return ""
	}
	if r.Namespace != "" {
		return r.Registry + "/" + r.Namespace + "/" + r.RepoName
	}
	return r.Registry + "/" + r.RepoName
}

func (r *ImageReference) FullName() string {
	if r == nil || r.Tag == "" {
		return r.FullRepoName()
	}
	return r.FullRepoName() + ":" + r.Tag
}

func parseReference(ref string, requireTag bool) *ImageReference {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}

	imageRef := &ImageReference{
		OriginalRef: ref,
	}

	imagePath := ref
	if requireTag {
		lastSlash := strings.LastIndex(ref, "/")
		lastColon := strings.LastIndex(ref, ":")
		imageRef.Tag = "latest"
		if lastColon > lastSlash {
			tag := ref[lastColon+1:]
			if tag == "" {
				return nil
			}
			imageRef.Tag = tag
			imagePath = ref[:lastColon]
		}
	}

	imageParts := strings.Split(imagePath, "/")
	if len(imageParts) == 0 {
		return nil
	}

	isRegistry := false
	if len(imageParts) > 1 {
		firstPart := imageParts[0]
		if strings.Contains(firstPart, ".") || strings.Contains(firstPart, ":") || firstPart == "localhost" {
			isRegistry = true
		}
	}

	if isRegistry {
		imageRef.Registry = imageParts[0]
		if len(imageParts) == 2 {
			imageRef.RepoName = imageParts[1]
			return imageRef
		}

		imageRef.Namespace = strings.Join(imageParts[1:len(imageParts)-1], "/")
		imageRef.RepoName = imageParts[len(imageParts)-1]
		return imageRef
	}

	imageRef.Registry = "docker.io"
	if len(imageParts) == 1 {
		imageRef.Namespace = "library"
		imageRef.RepoName = imageParts[0]
		return imageRef
	}

	imageRef.Namespace = strings.Join(imageParts[:len(imageParts)-1], "/")
	imageRef.RepoName = imageParts[len(imageParts)-1]
	return imageRef
}
