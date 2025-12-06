package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type OfferResponse struct {
	ID          uint    `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Distance    float64 `json:"distance_meters"`
}

var db *gorm.DB

func main() {
	// Cargar variables de entorno
	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ Advertencia: No se pudo cargar el archivo .env, buscando variables de entorno del sistema:", err)
	}

	// 2. CONEXIÓN A BASE DE DATOS
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("❌ Error: DATABASE_URL no está configurada en el archivo .env")
	}

	var err error
	db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("❌ Falló la conexión a la base de datos:", err)
	}
	log.Println("✅ Conectado a PostgreSQL + PostGIS en Render")

	// 3. CONFIGURAR EL ROUTER
	r := gin.Default()

	// Middleware CORS básico
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Next()
	})

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "online", "message": "Zona Flash API ⚡"})
	})

	r.GET("/api/offers", getNearbyOffers)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}

func getNearbyOffers(c *gin.Context) {
	latStr := c.Query("lat")
	lngStr := c.Query("lng")
	radiusStr := c.Query("radius")

	if latStr == "" || lngStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Faltan coordenadas (lat, lng)"})
		return
	}

	lat, _ := strconv.ParseFloat(latStr, 64)
	lng, _ := strconv.ParseFloat(lngStr, 64)
	radius, _ := strconv.ParseFloat(radiusStr, 64)
	if radius == 0 {
		radius = 5000
	}

	var offers []OfferResponse

	// Consulta PostGIS optimizada
	query := `
		SELECT 
			id, title, description, price, category,
			ST_Y(location::geometry) as latitude,
			ST_X(location::geometry) as longitude,
			ST_Distance(location, ST_MakePoint(?, ?)::geography) as distance_meters
		FROM offers
		WHERE ST_DWithin(location, ST_MakePoint(?, ?)::geography, ?)
		AND is_active = TRUE
		ORDER BY distance_meters ASC
		LIMIT 50;
	`

	result := db.Raw(query, lng, lat, lng, lat, radius).Scan(&offers)

	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
		return
	}

	c.JSON(http.StatusOK, offers)
}
