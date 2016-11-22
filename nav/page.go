package nav

import (
	"path/filepath"
	"regexp"
	"strings"
	"github.com/gruntwork-io/docs/errors"
	"github.com/gruntwork-io/docs/file"
	"bytes"
	"fmt"
	"github.com/shurcooL/github_flavored_markdown"
	"html/template"
)

const FILE_PATHS_REGEX = `(?:http:/|https:/)?(/[A-Za-z0-9_/.-]+)|([A-Za-z0-9_/.-]+/[A-Za-z0-9_.-]*)`
const PACKAGE_GITHUB_REPO_URL_PREFIX = "https://github.com/gruntwork-io/<package-name>/tree/master"
const PACKAGE_FILE_REGEX = `^packages/([\w -]+)(/.*)$`
const PACKAGE_FILE_REGEX_NUM_CAPTURE_GROUPS = 2
const MARKDOWN_FILE_PATH_REGEX = `^.*/(.*)\.md$`
const MARKDOWN_FILE_PATH_REGEX_NUM_CAPTURE_GROUPS = 1

// TODO: Figure out better way to reference this file
const HTML_TEMPLATE_REL_PATH = "_html/doc_template.html"

// A Page represents a page of documentation, usually formatted as a markdown file.
type Page struct {
	File
	Title        string  // the title of the page
	BodyMarkdown string  // the body of the page as Markdown
	BodyHtml     string  // the body of the page as HTML (does not include surrounding HTML)
	GithubUrl    string  // the Gruntwork Repo GitHub URL to which this page corresponds
	ParentFolder *Folder // the nav folder in which this page resides
}

// Populate all the remaining properties of this Page instance
func (p *Page) PopulateAllProperties() error {
	var err error

	p.Title = p.getTitle()

	p.BodyMarkdown, err = p.getSanitizedMarkdownBody()
	if err != nil {
		return errors.WithStackTrace(err)
	}

	p.BodyHtml = getHtmlFromMarkdown(p.BodyMarkdown)

	p.GithubUrl, err = convertPackageLinkToUrl(p.InputPath, "./")
	if err != nil {
		return errors.WithStackTrace(err)
	}

	return nil
}

// Add this page to the NavTree that starts at the rootFolder, creating any necessary folders along the way.
func (p *Page) AddToNavTree(rootFolder *Folder) error {
	containingFolderPath := getContainingFolder(p.OutputPath)
	containingFolder := rootFolder.CreateFolderIfNotExist(containingFolderPath)

	containingFolder.AddPage(p)

	return nil
}

// Get the folder that contains the file specified in the given path
func getContainingFolder(path string) string {
	return filepath.Dir(path)
}

// Output the full HTML body of this page
func (p *Page) WriteFullPageHtmlToOutputPath(rootFolder *Folder, rootOutputPath string) error {
	bodyHtml := p.getBodyHtml()
	navTreeHtml := p.getNavTreeHtml(rootFolder)

	fullHtml, err := getFullHtml(bodyHtml, navTreeHtml, p.Title)
	if err != nil {
		return errors.WithStackTrace(err)
	}

	absOutputPath := filepath.Join(rootOutputPath, p.OutputPath)
	absOutputPathDotHtml, err := replaceMdFileExtensionWithHtmlFileExtension(absOutputPath)
	if err != nil {
		return errors.WithStackTrace(err)
	}

	fmt.Printf("Outputting %s to %s\n", p.InputPath, absOutputPathDotHtml)

	err = file.WriteFile(fullHtml, absOutputPathDotHtml)
	if err != nil {
		return errors.WithStackTrace(err)
	}

	return nil
}

// Get the NavTree of the given Root Folder with the current page as the "active" page as HTML
func (p *Page) getNavTreeHtml(rootFolder *Folder) template.HTML {
	return rootFolder.GetAsNavTreeHtml(p)
}

// Get the NavTree of the givn Root Folder with the current page as the "active" page as HTML
func (p *Page) getBodyHtml() template.HTML {
	return template.HTML(p.BodyHtml)
}

// Return a NewPage
func NewPage(file *File) *Page {
	return &Page{
		File: *file,
	}
}

// Get the Page's title from the page's output filename.
func (p *Page) getTitle() string {
	fileNameFull := filepath.Base(p.OutputPath)
	fileNameComponents := strings.Split(fileNameFull, ".")
	title := fileNameComponents[0]
	return strings.Title(title)
}

// Get the Page's markdown body, sanitized for public HTML output (i.e. convert inline links to fully qualified URLs)
func (p *Page) getSanitizedMarkdownBody() (string, error) {
	var body string

	body, err := file.ReadFile(p.FullInputPath)
	if err != nil {
		return body, errors.WithStackTrace(err)
	}

	body, err = convertMarkdownLinksToUrls(p.InputPath, body)
	if err != nil {
		return body, errors.WithStackTrace(err)
	}

	return body, nil
}

// Given a doc file with the given body at the given inputPath, convert all paths in the body (e.g. "/foo" or "../bar")
// to fully qualified URLs.
func convertMarkdownLinksToUrls(inputPath, body string) (string, error) {
	var newBody string

	newBody = body
	linkPaths := getAllLinkPaths(body)

	for _, linkPath := range linkPaths {
		url, err := convertPackageLinkToUrl(inputPath, linkPath)
		if err != nil {
			return newBody, errors.WithStackTrace(err)
		}

		// If we blindly replace all instances of our linkPath, some of them will be found in existing URLs!
		// So we wrap them in parentheses (to support markdown-formatted links)...
		linkPathWithParens := fmt.Sprintf("(%s)", linkPath)
		urlWithParens := fmt.Sprintf("(%s)", url)

		newBody = strings.Replace(newBody, linkPathWithParens, urlWithParens, -1)

		// ... and spaces (to support standalone links).
		linkPathWithSpaces := fmt.Sprintf(" %s ", linkPath)
		urlWithSpaces := fmt.Sprintf(" %s ", url)

		newBody = strings.Replace(newBody, linkPathWithSpaces, urlWithSpaces, -1)
	}

	return newBody, nil
}

// Given a body of text find all instances of link paths (e.g. /foo or ../bar)
func getAllLinkPaths(body string) []string {
	var relPaths []string

	regex := regexp.MustCompile(FILE_PATHS_REGEX)
	submatches := regex.FindAllStringSubmatch(body, -1)

	if len(submatches) == 0 {
		return relPaths
	}

	for _, submatch := range submatches {
		relPath := submatch[0]

		// Cowardly use string search because Golang regular expressions don't support positive lookahead.
		if ! strings.Contains(relPath, "http://") && ! strings.Contains(relPath, "https://") {
			relPaths = append(relPaths, relPath)
		}
	}

	return relPaths
}

// Convert a link that directs to another Package page to a fully qualified URL. For non-Package links, just return the original link
func convertPackageLinkToUrl(inputPath, linkPath string) (string, error) {
	var url string

	if isPackageInputPath(inputPath) {
		packageName, err := getPackageName(inputPath)
		if err != nil {
			return url, errors.WithStackTrace(err)
		}

		urlPrefix := strings.Replace(PACKAGE_GITHUB_REPO_URL_PREFIX, "<package-name>", packageName, 1)

		if strings.HasPrefix(linkPath, "/") {
			url = urlPrefix + linkPath
		}

		if strings.HasPrefix(linkPath, "./") {
			relPath, err := getPathRelativeToPackageRoot(inputPath)
			if err != nil {
				return url, errors.WithStackTrace(err)
			}

			url = urlPrefix + relPath
		}

		if strings.HasPrefix(linkPath, "../") {
			relPath, err := getPathRelativeToPackageRoot(inputPath)
			if err != nil {
				return url, errors.WithStackTrace(err)
			}

			relPath = filepath.Join(relPath, "../", linkPath)
			url = urlPrefix + relPath
		}

		return url, nil
	}

	// If this isn't a Package Page, just output the linkPath unmodified
	return linkPath, nil
}

// Return true if the given inputPath is part of a Gruntwork Package
func isPackageInputPath(inputPath string) bool {
	regex := regexp.MustCompile(PACKAGE_FILE_REGEX)
	return regex.MatchString(inputPath)
}

// Extract the Package name from the given inputPath
func getPackageName(inputPath string) (string, error) {
	var subpath string

	regex := regexp.MustCompile(PACKAGE_FILE_REGEX)
	submatches := regex.FindAllStringSubmatch(inputPath, -1)

	if len(submatches) == 0 || len(submatches[0]) != PACKAGE_FILE_REGEX_NUM_CAPTURE_GROUPS + 1 {
		return subpath, errors.WithStackTrace(&WrongNumberOfCaptureGroupsReturnedFromPageRegEx{inputPath: inputPath, regExName: "PACKAGE_FILE_REGEX", regEx: PACKAGE_FILE_REGEX })
	}

	subpath = submatches[0][1]

	return subpath, nil
}

// Given an inputPath like packages/package-vpc/modules/vpc-app, extract the path relative to the package root (i.e. /modules/vpc-app)
func getPathRelativeToPackageRoot(inputPath string) (string, error) {
	var subpath string

	regex := regexp.MustCompile(PACKAGE_FILE_REGEX)
	submatches := regex.FindAllStringSubmatch(inputPath, -1)

	if len(submatches) == 0 || len(submatches[0]) != PACKAGE_FILE_REGEX_NUM_CAPTURE_GROUPS + 1 {
		return subpath, errors.WithStackTrace(&WrongNumberOfCaptureGroupsReturnedFromPageRegEx{inputPath: inputPath, regExName: "PACKAGE_FILE_REGEX", regEx: PACKAGE_FILE_REGEX })
	}

	subpath = submatches[0][2]

	return subpath, nil
}

// Given a markdown body return an HTML body
func getHtmlFromMarkdown(markdown string) string {
	bytesInput := []byte(markdown)
	bytesOutput := github_flavored_markdown.Markdown(bytesInput)
	return string(bytesOutput)
}

// Given a path like /foo/bar.md, return /foo/bar.html
func replaceMdFileExtensionWithHtmlFileExtension(path string) (string, error) {
	var updatedPath string

	regex := regexp.MustCompile(MARKDOWN_FILE_PATH_REGEX)
	submatches := regex.FindAllStringSubmatch(path, -1)

	if len(submatches) == 0 || len(submatches[0]) != MARKDOWN_FILE_PATH_REGEX_NUM_CAPTURE_GROUPS + 1 {
		return updatedPath, errors.WithStackTrace(&WrongNumberOfCaptureGroupsReturnedFromPageRegEx{inputPath: path, regExName: "MARKDOWN_FILE_PATH_REGEX", regEx: MARKDOWN_FILE_PATH_REGEX })
	}

	filename := submatches[0][1]
	filenameDotMd := fmt.Sprintf("%s.%s", filename, "md")
	filenameDotHtml := fmt.Sprintf("%s.%s", filename, "html")

	updatedPath = strings.Replace(path, filenameDotMd, filenameDotHtml, -1)

	return updatedPath, nil
}

// Return the full HTML rendering of this page
func getFullHtml(pageBodyHtml template.HTML, navTreeHtml template.HTML, pageTitle string) (string, error) {
	var templateOutput string

	type htmlTemplateProperties struct {
		PageTitle string
		PageBody  template.HTML
		NavTree   template.HTML
	}

	htmlTemplatePath := filepath.Join(HTML_TEMPLATE_REL_PATH)
	htmlTemplateBody, err := file.ReadFile(htmlTemplatePath)
	if err != nil {
		return templateOutput, errors.WithStackTrace(err)
	}

	htmlTemplate, err := template.New(pageTitle).Parse(htmlTemplateBody)
	if err != nil {
		return templateOutput, errors.WithStackTrace(err)
	}

	buf := new(bytes.Buffer)
	htmlTemplate.Execute(buf, &htmlTemplateProperties{
		PageTitle: pageTitle,
		PageBody: pageBodyHtml,
		NavTree: navTreeHtml,
	})

	templateOutput = buf.String()

	return templateOutput, nil
}