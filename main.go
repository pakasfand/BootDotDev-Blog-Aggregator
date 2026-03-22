package main

import (
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/pakasfand/BootDotDev-Blog-Aggregator/internal/config"
	"github.com/pakasfand/BootDotDev-Blog-Aggregator/internal/database"

	_ "github.com/lib/pq"
)

type State struct {
	Config *config.Config
	db  *database.Queries
}

type Command struct {
	Name string
	Arguments []string
}

type RSSFeed struct {
	Channel struct {
		Title       string    `xml:"title"`
		Link        string    `xml:"link"`
		Description string    `xml:"description"`
		Item        []RSSItem `xml:"item"`
	} `xml:"channel"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
}

func fetchFeed(ctx context.Context, feedURL string) (*RSSFeed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		fmt.Printf("Request creation failed: %v\n", err)
		return nil, err
	}

	client := http.Client{}
	res, err := client.Do(req)
	if err != nil {
		fmt.Printf("Request failed %v\n", err)
		return nil, err
	}

	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Printf("Failed to read response body %v\n", err)
		return nil, err
	}

	var feed RSSFeed
	err = xml.Unmarshal(body, &feed)
	if err != nil {
		fmt.Printf("Failed to unmarshal response body %v\n", err)
		return nil, err
	}
	
	feed.Channel.Title = html.UnescapeString(feed.Channel.Title)
	feed.Channel.Description = html.UnescapeString(feed.Channel.Description)
	for i, item := range feed.Channel.Item {
		item.Title = html.UnescapeString(item.Title)
		item.Description = html.UnescapeString(item.Description)
		feed.Channel.Item[i] = item
	}
	return &feed, nil
}

func middlewareLoggedIn(handler func(s *State, cmd Command, user database.User) error) func(*State, Command) error {
	return func(s *State, cmd Command) error {
		if s.Config.CurrentUserName == "" {
			return fmt.Errorf("no user is currently logged in")
		}

		user, err := s.db.GetUser(context.Background(), s.Config.CurrentUserName)
		if err != nil {
			return fmt.Errorf("failed to get current user %s: %w", s.Config.CurrentUserName, err)
		}

		return handler(s, cmd, user)
	}
}

func handlerLogin(s *State, cmd Command) error {
	if len(cmd.Arguments) != 1 {
		return fmt.Errorf("usage: %s <name>", cmd.Name)
	}
	
	userName := cmd.Arguments[0]
	
	_, err := s.db.GetUser(context.Background(), userName)
	if err != nil {
		return fmt.Errorf("Failed to login user %s", userName)
	}

	fmt.Printf("Logging in as %s\n", userName)
	config.SetUser(userName)
	return nil
}

func handlerRegister(s *State, cmd Command) error {
	if len(cmd.Arguments) != 1 {
		return fmt.Errorf("usage: %s <name>", cmd.Name)
	}

	userName := cmd.Arguments[0]

	_, err := s.db.GetUser(context.Background(), userName)
	if err == nil {
		return fmt.Errorf("Failed to register user %s", userName)
	}

	user, _ := s.db.CreateUser(context.Background(), database.CreateUserParams{
		ID: uuid.New(),
		CreatedAt: sql.NullTime{Time: time.Now()},
		UpdatedAt: sql.NullTime{Time: time.Now()},
		Name: userName,
	})

	config.SetUser(userName);
	fmt.Printf("User %s registered successfully", userName)
	fmt.Println(user)
	return nil
}

func handlerReset(s *State, cmd Command) error {
	err := s.db.DeleteUsers(context.Background())
	if err != nil {
		return fmt.Errorf("Failed to reset users")
	}

	fmt.Println("Successfully reset users")
	return nil
}

func handlerUsers(s *State, cmd Command) error {
	users, err := s.db.GetUsers(context.Background())
	if err != nil {
		return fmt.Errorf("Failed to get users")
	}

	for _, user := range users {
		if user.Name == s.Config.CurrentUserName {
			fmt.Printf("* %s (current)\n", user.Name)
		} else {
			fmt.Printf("* %s\n", user.Name)
		}
	}
	return nil
}

func scrapeFeeds(s *State) error {
	feedToFetch, err := s.db.GetNextFeedToFetch(context.Background())
	if err != nil {
		return err
	}

	err = s.db.MarkFeedFetched(context.Background(), database.MarkFeedFetchedParams{
		ID: feedToFetch.ID,
		LastFetchedAt: sql.NullTime{
			Time: time.Now(), 
			Valid: true,
		},
	})
	if err != nil {
		return err
	}

	feed, err := fetchFeed(context.Background(), feedToFetch.Url)
	if err != nil {
		return err
	}

	for _, feedItem := range feed.Channel.Item {
		publishedAt := sql.NullTime{}
		if feedItem.PubDate != "" {
			parsedTime, err := time.Parse(time.RFC1123Z, feedItem.PubDate)
			if err == nil {
				publishedAt = sql.NullTime{Time: parsedTime, Valid: true}
			}
		}

		s.db.CreatePost(context.Background(), database.CreatePostParams{
			ID: uuid.New(),
			CreatedAt: sql.NullTime{Time: time.Now()},
			UpdatedAt: sql.NullTime{Time: time.Now()},
			Title: feedItem.Title,
			Url: feedItem.Link,
			Description: sql.NullString{String: feedItem.Description, Valid: feedItem.Description != ""},
			PublishedAt: publishedAt,
			FeedID: feedToFetch.ID,
		})
	}

	return nil
}

func handlerAggregator(s *State, cmd Command) error {
	if len(cmd.Arguments) != 1 {
		return fmt.Errorf("usage: %s <time_between_reqs>", cmd.Name)
	}

	timeBetweenRequests, err := time.ParseDuration(cmd.Arguments[0])
	if err != nil {
		return err
	}

	fmt.Printf("Collecting feeds every %v\n", timeBetweenRequests)

	ticker := time.NewTicker(timeBetweenRequests)
	for ; ; <-ticker.C {
		err := scrapeFeeds(s)
		if err != nil {
			return err
		}
	}
}

func handlerAddFeed(s *State, cmd Command, user database.User) error {
	if len(cmd.Arguments) != 2 {
		return fmt.Errorf("usage: %s <name> <url>", cmd.Name)
	}

	feedName := cmd.Arguments[0]
	url := cmd.Arguments[1]

	feed, err := s.db.CreateFeed(context.Background(), database.CreateFeedParams{
		ID: uuid.New(),
		CreatedAt: sql.NullTime{Time: time.Now()},
		UpdatedAt: sql.NullTime{Time: time.Now()},
		Name: feedName,
		Url: url,
		UserID: user.ID,
	})
	if err != nil {
		return err
	}

	s.db.CreateFeedFollow(context.Background(), database.CreateFeedFollowParams{
		ID: uuid.New(),
		CreatedAt: sql.NullTime{Time: time.Now()},
		UpdatedAt: sql.NullTime{Time: time.Now()},
		UserID: user.ID,
		FeedID: feed.ID,
	})

	fmt.Println(feed)
	return nil
}

func handlerFeeds(s *State, cmd Command) error {
	feeds, err := s.db.GetFeeds(context.Background())
	if err != nil {
		return err
	}

	for _, feed := range feeds {
		user, _ := s.db.GetUserById(context.Background(), feed.UserID)
		fmt.Printf("%v %v %v\n", feed.Name, feed.Url, user.Name)
	}
	return nil
}

func handlerFollow(s *State, cmd Command, user database.User) error {
	if len(cmd.Arguments) != 1 {
		return fmt.Errorf("usage: %s <url>", cmd.Name)
	}

	url := cmd.Arguments[0]
	feed, _ := s.db.GetFeedByUrl(context.Background(), url)

	s.db.CreateFeedFollow(context.Background(), database.CreateFeedFollowParams{
		ID: uuid.New(),
		CreatedAt: sql.NullTime{Time: time.Now()},
		UpdatedAt: sql.NullTime{Time: time.Now()},
		UserID: user.ID,
		FeedID: feed.ID,
	})
	
	fmt.Printf("%v %v\n", feed.Name, user.Name)
	return nil
}

func handlerFollowing(s *State, cmd Command, user database.User) error {
	fmt.Printf("UserID: %v\n", user.ID)
	feedFollows, _ := s.db.GetFeedFollowsForUser(context.Background(), user.ID)
	fmt.Println(feedFollows)
	for _, feedFollow := range feedFollows {
		fmt.Printf("- %v\n", feedFollow.FeedName)
	}
	return nil
}

func handlerUnfollow(s *State, cmd Command, user database.User) error {
	url := cmd.Arguments[0]
	feed, _ := s.db.GetFeedByUrl(context.Background(), url)

	err := s.db.DeleteFeedFollow(context.Background(), database.DeleteFeedFollowParams{
		FeedID: feed.ID,
		UserID: user.ID, 
	})

	return err
}

func handlerBrowse(s *State, cmd Command, user database.User) error {
	limit := 2
	if len(cmd.Arguments) == 1 {
		limit, _ = strconv.Atoi(cmd.Arguments[0])
	}

	posts, err := s.db.GetPostsForUser(context.Background(), database.GetPostsForUserParams{
		UserID: user.ID,
		Limit: int32(limit),
	})
	if err != nil {
		return err
	}

	for _, post := range posts {
		fmt.Println(post)
	}

	return nil
}

type Commands struct {
	Registry map[string]func(*State, Command) error
}

func (c *Commands) run(s *State, cmd Command) error{
	commandHandler, found := c.Registry[cmd.Name]
	if !found {
		return fmt.Errorf("Command with name {%s} not found!", cmd.Name)
	}
	
	return commandHandler(s, cmd)
}

func (c *Commands) register(name string, f func(*State, Command) error) {
	c.Registry[name] = f
}

func main() {
	commands := Commands{
		Registry: make(map[string]func(*State, Command) error),
	}
	commands.register("login", handlerLogin)
	commands.register("register", handlerRegister)
	commands.register("reset", handlerReset)
	commands.register("users", handlerUsers)
	commands.register("agg", handlerAggregator)
	commands.register("addfeed", middlewareLoggedIn(handlerAddFeed))
	commands.register("feeds", handlerFeeds)
	commands.register("follow", middlewareLoggedIn(handlerFollow))
	commands.register("following", middlewareLoggedIn(handlerFollowing))
	commands.register("unfollow", middlewareLoggedIn(handlerUnfollow))
	commands.register("browse", middlewareLoggedIn(handlerBrowse))

	config := config.Read()
	state := State {
		Config: &config,
	}

	db, err := sql.Open("postgres", config.DBUrl)
	if err != nil {
		fmt.Println("Failed to connect to the database!")
		os.Exit(1)
	}
	state.db = database.New(db)

	if len(os.Args) < 2 {
		fmt.Println("Not enough arguments!")
		os.Exit(1)
	}
	
	commandName := os.Args[1]
	commandArgs := os.Args[2:]
	
	err = commands.run(&state, 
		Command {
			Name: commandName, 
			Arguments: commandArgs,
		},
	)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}