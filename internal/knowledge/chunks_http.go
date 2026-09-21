package knowledge

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

func registerChunkIndexes(group *gin.RouterGroup, store *Store) {
	ids := func(c *gin.Context) (uint64, uint64, bool) {
		library, ok := resourceID(c, "library_id")
		if !ok {
			return 0, 0, false
		}
		source, ok := resourceID(c, "source_id")
		return library, source, ok
	}
	group.GET("/libraries/:library_id/sources/:source_id/index", func(c *gin.Context) {
		library, source, ok := ids(c)
		if !ok {
			return
		}
		status, err := store.SourceIndex(c.Request.Context(), c.GetUint("user_id"), library, source)
		if !respondError(c, err) {
			c.JSON(200, status)
		}
	})
	group.POST("/libraries/:library_id/sources/:source_id/index/rebuild", func(c *gin.Context) {
		library, source, ok := ids(c)
		if !ok {
			return
		}
		index, err := store.RebuildIndex(c.Request.Context(), c.GetUint("user_id"), library, source)
		if !respondError(c, err) {
			c.JSON(200, IndexStatus{State: "ready", Index: index})
		}
	})
	group.GET("/libraries/:library_id/sources/:source_id/versions/:version_id/chunks", func(c *gin.Context) {
		library, source, ok := ids(c)
		if !ok {
			return
		}
		version, ok := resourceID(c, "version_id")
		if !ok {
			return
		}
		after := 0
		if raw, exists := c.GetQuery("after"); exists {
			var err error
			after, err = strconv.Atoi(raw)
			if err != nil || after < 0 {
				respondError(c, ErrInvalid)
				return
			}
		}
		chunks, err := store.Chunks(c.Request.Context(), c.GetUint("user_id"), library, source, version, after)
		if !respondError(c, err) {
			c.JSON(200, gin.H{"version_id": version, "items": chunks})
		}
	})
}
